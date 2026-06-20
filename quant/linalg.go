package quant

import "math"

// matVecMul computes out = M * vec where M is a row-major n*n matrix
// (spec 09 §4.3, the OPQ rotation z = R*x).
func matVecMul(m, vec, out []float32, n int) {
	for i := 0; i < n; i++ {
		var s float32
		row := m[i*n : (i+1)*n]
		for j := 0; j < n; j++ {
			s += row[j] * vec[j]
		}
		out[i] = s
	}
}

// matVecMulT computes out = M^T * vec where M is a row-major n*n matrix. For an
// orthogonal R this inverts the rotation, since R^-1 = R^T (spec 09 §4.3).
func matVecMulT(m, vec, out []float32, n int) {
	for i := 0; i < n; i++ {
		out[i] = 0
	}
	for j := 0; j < n; j++ {
		vj := vec[j]
		row := m[j*n : (j+1)*n]
		for i := 0; i < n; i++ {
			out[i] += row[i] * vj
		}
	}
}

// procrustes solves the orthogonal Procrustes problem: given the cross-covariance
// matrix A = sum_i y_i x_i^T (row-major n*n), it returns the orthogonal R that
// minimizes sum_i ||R x_i - y_i||^2, which is R = U V^T where A = U S V^T
// (spec 09 §4.2). It is the rotation-update step of OPQ training.
func procrustes(a []float32, n int) []float32 {
	u, _, v := jacobiSVD(a, n)
	// R = U V^T.
	r := make([]float32, n*n)
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			var s float32
			for k := 0; k < n; k++ {
				s += u[i*n+k] * v[j*n+k] // V^T[k][j] = V[j][k]
			}
			r[i*n+j] = s
		}
	}
	return r
}

// jacobiSVD computes a singular value decomposition A = U S V^T of a square
// row-major n*n matrix using one-sided Jacobi rotations applied to A's columns.
// It returns U (n*n row-major), the singular values S (length n), and V (n*n
// row-major). One-sided Jacobi is compact and numerically robust; it is used only
// at OPQ training time, so its O(n^3) per sweep cost is acceptable.
func jacobiSVD(a []float32, n int) (u []float32, s []float32, v []float32) {
	// Work in float64 for stability. Columns of the working matrix are orthogonalized.
	w := make([]float64, n*n)
	for i := range a {
		w[i] = float64(a[i])
	}
	vv := make([]float64, n*n)
	for i := 0; i < n; i++ {
		vv[i*n+i] = 1
	}

	const maxSweeps = 60
	const eps = 1e-12
	for sweep := 0; sweep < maxSweeps; sweep++ {
		off := 0.0
		for p := 0; p < n-1; p++ {
			for q := p + 1; q < n; q++ {
				var alpha, beta, gamma float64
				for i := 0; i < n; i++ {
					ap := w[i*n+p]
					aq := w[i*n+q]
					alpha += ap * ap
					beta += aq * aq
					gamma += ap * aq
				}
				off += math.Abs(gamma)
				if math.Abs(gamma) < eps*math.Sqrt(alpha*beta) || alpha == 0 || beta == 0 {
					continue
				}
				zeta := (beta - alpha) / (2 * gamma)
				t := sign(zeta) / (math.Abs(zeta) + math.Sqrt(1+zeta*zeta))
				c := 1 / math.Sqrt(1+t*t)
				sn := c * t
				// Rotate columns p and q of w and of vv.
				for i := 0; i < n; i++ {
					ap := w[i*n+p]
					aq := w[i*n+q]
					w[i*n+p] = c*ap - sn*aq
					w[i*n+q] = sn*ap + c*aq
					vp := vv[i*n+p]
					vq := vv[i*n+q]
					vv[i*n+p] = c*vp - sn*vq
					vv[i*n+q] = sn*vp + c*vq
				}
			}
		}
		if off < eps {
			break
		}
	}

	// Singular values are the column norms of w; U's columns are w normalized.
	s = make([]float32, n)
	u = make([]float32, n*n)
	v = make([]float32, n*n)
	for j := 0; j < n; j++ {
		var norm float64
		for i := 0; i < n; i++ {
			norm += w[i*n+j] * w[i*n+j]
		}
		norm = math.Sqrt(norm)
		s[j] = float32(norm)
		if norm > 1e-300 {
			for i := 0; i < n; i++ {
				u[i*n+j] = float32(w[i*n+j] / norm)
			}
		} else {
			u[j*n+j] = 1 // degenerate column: fall back to a unit vector
		}
		for i := 0; i < n; i++ {
			v[i*n+j] = float32(vv[i*n+j])
		}
	}
	return u, s, v
}

func sign(x float64) float64 {
	if x < 0 {
		return -1
	}
	return 1
}
