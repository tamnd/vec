package bulk

import (
	"bufio"
	"io"
	"strings"
)

// gzipMagic reports whether the buffered reader starts with the gzip magic bytes,
// without consuming them.
func gzipMagic(br *bufio.Reader) bool {
	b, err := br.Peek(2)
	if err != nil {
		return false
	}
	return b[0] == 0x1f && b[1] == 0x8b
}

// stmtScanner splits a VectorSQL dump into statements at top-level semicolons. It
// tracks single-quoted string literals (a doubled quote escapes one), double-quoted
// identifiers, and -- line comments so a semicolon inside any of those does not end
// a statement. It reads from the underlying reader incrementally, holding at most
// one statement in memory at a time.
type stmtScanner struct {
	r   *bufio.Reader
	buf strings.Builder
}

func newStmtScanner(r *bufio.Reader) *stmtScanner {
	return &stmtScanner{r: r}
}

// next returns the next statement text (without the trailing semicolon), or io.EOF
// when the input is exhausted with no remaining statement.
func (s *stmtScanner) next() (string, error) {
	s.buf.Reset()
	var (
		inString  bool
		inIdent   bool
		inComment bool
		any       bool
	)
	for {
		c, err := s.r.ReadByte()
		if err == io.EOF {
			if any && strings.TrimSpace(s.buf.String()) != "" {
				return s.buf.String(), nil
			}
			return "", io.EOF
		}
		if err != nil {
			return "", err
		}
		any = true

		if inComment {
			if c == '\n' {
				inComment = false
				s.buf.WriteByte(c)
			}
			continue
		}
		switch {
		case inString:
			s.buf.WriteByte(c)
			if c == '\'' {
				// Look ahead for a doubled quote escape.
				nb, err := s.r.Peek(1)
				if err == nil && nb[0] == '\'' {
					_, _ = s.r.ReadByte()
					s.buf.WriteByte('\'')
					continue
				}
				inString = false
			}
			continue
		case inIdent:
			s.buf.WriteByte(c)
			if c == '"' {
				inIdent = false
			}
			continue
		}

		switch c {
		case '\'':
			inString = true
			s.buf.WriteByte(c)
		case '"':
			inIdent = true
			s.buf.WriteByte(c)
		case '-':
			// A second dash starts a line comment.
			nb, err := s.r.Peek(1)
			if err == nil && nb[0] == '-' {
				_, _ = s.r.ReadByte()
				inComment = true
				continue
			}
			s.buf.WriteByte(c)
		case ';':
			return s.buf.String(), nil
		default:
			s.buf.WriteByte(c)
		}
	}
}
