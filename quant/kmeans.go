package quant

import (
	"math"
	"math/rand"

	"github.com/tamnd/vector/distance"
)

// kmeans runs Lloyd's algorithm to fit k centroids to the rows of data (spec 09
// §3.3). data is n rows of dim columns, row-major. It returns the centroids as k
// rows of dim columns, row-major. Empty clusters are reseeded to the point
// farthest from its assigned centroid so k centroids are always returned. The rng
// makes the run reproducible: PQ training seeds it deterministically so a codebook
// is a pure function of its training sample (spec 09 §8.3, codebook immutability).
func kmeans(data []float32, n, dim, k, iters int, rng *rand.Rand) []float32 {
	if k > n {
		k = n
	}
	centroids := make([]float32, k*dim)
	kmeansPlusPlusInit(data, n, dim, k, centroids, rng)

	assign := make([]int, n)
	counts := make([]int, k)
	for it := 0; it < iters; it++ {
		changed := assignStep(data, n, dim, k, centroids, assign)
		updateStep(data, n, dim, k, centroids, assign, counts, rng)
		if !changed && it > 0 {
			break // converged: no point switched clusters
		}
	}
	return centroids
}

// kmeansPlusPlusInit seeds centroids with the k-means++ rule: the first centroid
// is a random point, each subsequent centroid is chosen with probability
// proportional to its squared distance to the nearest already-chosen centroid.
// This gives a well-spread start and far fewer Lloyd iterations than random seeding.
func kmeansPlusPlusInit(data []float32, n, dim, k int, centroids []float32, rng *rand.Rand) {
	first := rng.Intn(n)
	copy(centroids[:dim], data[first*dim:(first+1)*dim])

	dist2 := make([]float32, n)
	for i := range dist2 {
		dist2[i] = math.MaxFloat32
	}
	for c := 1; c < k; c++ {
		// Update each point's distance to the nearest chosen centroid.
		prev := centroids[(c-1)*dim : c*dim]
		var total float64
		for i := 0; i < n; i++ {
			d := distance.L2SquaredFloat32(data[i*dim:(i+1)*dim], prev)
			if d < dist2[i] {
				dist2[i] = d
			}
			total += float64(dist2[i])
		}
		// Sample the next centroid proportional to dist2.
		target := rng.Float64() * total
		pick := n - 1
		var acc float64
		for i := 0; i < n; i++ {
			acc += float64(dist2[i])
			if acc >= target {
				pick = i
				break
			}
		}
		copy(centroids[c*dim:(c+1)*dim], data[pick*dim:(pick+1)*dim])
	}
}

// assignStep assigns every point to its nearest centroid and reports whether any
// assignment changed since the last pass.
func assignStep(data []float32, n, dim, k int, centroids []float32, assign []int) bool {
	changed := false
	for i := 0; i < n; i++ {
		row := data[i*dim : (i+1)*dim]
		best, bestD := 0, float32(math.MaxFloat32)
		for c := 0; c < k; c++ {
			d := distance.L2SquaredFloat32(row, centroids[c*dim:(c+1)*dim])
			if d < bestD {
				bestD, best = d, c
			}
		}
		if assign[i] != best {
			assign[i] = best
			changed = true
		}
	}
	return changed
}

// updateStep recomputes each centroid as the mean of its assigned points. An
// empty cluster is reseeded to the point farthest from its own centroid so the
// codebook keeps all k entries (spec 09 §3.3).
func updateStep(data []float32, n, dim, k int, centroids []float32, assign, counts []int, rng *rand.Rand) {
	for c := 0; c < k; c++ {
		counts[c] = 0
	}
	for i := range centroids {
		centroids[i] = 0
	}
	for i := 0; i < n; i++ {
		c := assign[i]
		counts[c]++
		base := c * dim
		row := data[i*dim : (i+1)*dim]
		for j := 0; j < dim; j++ {
			centroids[base+j] += row[j]
		}
	}
	for c := 0; c < k; c++ {
		if counts[c] == 0 {
			reseedEmpty(data, n, dim, centroids, assign, c, rng)
			continue
		}
		inv := 1.0 / float32(counts[c])
		base := c * dim
		for j := 0; j < dim; j++ {
			centroids[base+j] *= inv
		}
	}
}

// reseedEmpty moves an empty centroid onto the worst-fit point so no cluster is
// wasted.
func reseedEmpty(data []float32, n, dim int, centroids []float32, assign []int, c int, rng *rand.Rand) {
	worst, worstD := rng.Intn(n), float32(-1)
	for i := 0; i < n; i++ {
		a := assign[i]
		if a == c {
			continue
		}
		d := distance.L2SquaredFloat32(data[i*dim:(i+1)*dim], centroids[a*dim:(a+1)*dim])
		if d > worstD {
			worstD, worst = d, i
		}
	}
	copy(centroids[c*dim:(c+1)*dim], data[worst*dim:(worst+1)*dim])
}
