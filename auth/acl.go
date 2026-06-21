// Package auth implements the server authentication and authorization surface
// from spec 23 sections 6 and 7: API keys, JWT validation, mutual-TLS principal
// mapping, local password accounts, the role model, and the collection-scoped
// access control list. It is a standalone package with no network code; the
// server (spec 16) composes it into its request pipeline. The embedded library
// and the CLI have no network surface and do not use it.
package auth

import (
	"fmt"
	"strings"
)

// Op is an operation a principal may attempt on a collection (spec 23 section
// 7.2). The role model grants sets of these.
type Op string

const (
	OpSearch         Op = "SEARCH"
	OpGet            Op = "GET"
	OpList           Op = "LIST"
	OpExplain        Op = "EXPLAIN"
	OpInsert         Op = "INSERT"
	OpUpsert         Op = "UPSERT"
	OpDelete         Op = "DELETE"
	OpCreateIndex    Op = "CREATE_INDEX"
	OpDropIndex      Op = "DROP_INDEX"
	OpCreateColl     Op = "CREATE_COLLECTION"
	OpDropColl       Op = "DROP_COLLECTION"
	OpTruncate       Op = "TRUNCATE"
	OpBackup         Op = "BACKUP"
	OpPragma         Op = "PRAGMA"
	OpKeyManagement  Op = "KEY_MANAGEMENT"
	OpServerConfig   Op = "SERVER_CONFIG"
	OpUserManagement Op = "USER_MANAGEMENT"
)

// Role names a set of permissions (spec 23 section 7.2).
type Role string

const (
	RoleReader    Role = "reader"
	RoleWriter    Role = "writer"
	RoleAdmin     Role = "admin"
	RoleKeyAdmin  Role = "key_admin"
	RoleSuperuser Role = "superuser"
)

// rolePerms is the static permission table (spec 23 section 7.2). writer extends
// reader, admin extends writer; the table is flattened so a lookup is one map
// read with no inheritance walk at request time.
var rolePerms = map[Role]map[Op]bool{
	RoleReader:   setOf(OpSearch, OpGet, OpList, OpExplain),
	RoleWriter:   setOf(OpSearch, OpGet, OpList, OpExplain, OpInsert, OpUpsert, OpDelete, OpCreateIndex, OpDropIndex),
	RoleAdmin:    setOf(OpSearch, OpGet, OpList, OpExplain, OpInsert, OpUpsert, OpDelete, OpCreateIndex, OpDropIndex, OpCreateColl, OpDropColl, OpTruncate, OpBackup, OpPragma),
	RoleKeyAdmin: setOf(OpKeyManagement),
}

func setOf(ops ...Op) map[Op]bool {
	m := make(map[Op]bool, len(ops))
	for _, op := range ops {
		m[op] = true
	}
	return m
}

// Allows reports whether the role grants the operation. The superuser grants
// everything; key_admin grants only key operations and does not imply admin (spec
// 23 section 7.2).
func (r Role) Allows(op Op) bool {
	if r == RoleSuperuser {
		return true
	}
	return rolePerms[r][op]
}

// Valid reports whether r is a known role.
func (r Role) Valid() bool {
	switch r {
	case RoleReader, RoleWriter, RoleAdmin, RoleKeyAdmin, RoleSuperuser:
		return true
	default:
		return false
	}
}

// Principal is an authenticated identity: an API key label, a JWT subject, or an
// mTLS common name (spec 23 section 7.2).
type Principal struct {
	ID   string
	Kind string // "apikey", "jwt", "mtls", "user"
}

// Binding ties a principal to a role over a collection scope (spec 23 section
// 7.2). The scope is a specific collection name, a glob like "docs_*", or "*".
type Binding struct {
	PrincipalID    string
	Role           Role
	CollectionGlob string
}

// CollectionMatches reports whether the binding's scope covers a collection name.
// The glob supports a single trailing "*" and the bare "*" wildcard, which is the
// scope syntax spec 23 section 7.3 specifies.
func (b Binding) CollectionMatches(collection string) bool {
	return globMatch(b.CollectionGlob, collection)
}

func globMatch(pattern, name string) bool {
	if pattern == "*" {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(name, strings.TrimSuffix(pattern, "*"))
	}
	return pattern == name
}

// ACL holds the role bindings and answers authorization checks (spec 23 section
// 7.3). It is safe for concurrent reads after construction; mutations are guarded
// by the caller or done before the server starts serving.
type ACL struct {
	bindings map[string][]Binding // principal id -> bindings
}

// NewACL builds an empty access control list.
func NewACL() *ACL {
	return &ACL{bindings: make(map[string][]Binding)}
}

// Grant adds a role binding for a principal over a collection scope.
func (a *ACL) Grant(principalID string, role Role, collectionGlob string) error {
	if !role.Valid() {
		return fmt.Errorf("vec/auth: unknown role %q", role)
	}
	a.bindings[principalID] = append(a.bindings[principalID], Binding{
		PrincipalID:    principalID,
		Role:           role,
		CollectionGlob: collectionGlob,
	})
	return nil
}

// Revoke removes every binding for a principal.
func (a *ACL) Revoke(principalID string) {
	delete(a.bindings, principalID)
}

// BindingsFor returns the bindings registered for a principal.
func (a *ACL) BindingsFor(p Principal) []Binding {
	return a.bindings[p.ID]
}

// Check authorizes an operation on a collection (spec 23 section 7.3). It returns
// ErrForbidden if no binding grants the operation. The error does not say whether
// the collection exists, so an unprivileged caller cannot probe the namespace.
func (a *ACL) Check(p Principal, op Op, collection string) error {
	for _, b := range a.bindings[p.ID] {
		if b.Role.Allows(op) && b.CollectionMatches(collection) {
			return nil
		}
	}
	return &ErrForbidden{Principal: p.ID, Op: op, Collection: collection}
}

// ErrForbidden is the authorization failure (spec 23 section 7.3). Its message is
// deliberately generic to avoid leaking collection existence.
type ErrForbidden struct {
	Principal  string
	Op         Op
	Collection string
}

func (e *ErrForbidden) Error() string { return "vec/auth: access denied" }
