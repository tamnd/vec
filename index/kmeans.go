package index

import (
	"math"
	"math/rand"

	"github.com/tamnd/vector/distance"
)

// trainCentroids fits k coarse centroids to the rows of data with k-means++ init
// and Lloyd iterations (spec 08 §3.3). data is n rows of dim columns, row-major;
// it returns k rows of dim columns, row-major. The coarse quantizer always ranks
// by L2 regardless of the index metric, because a Voronoi partition is defined by
// Euclidean proximity to centroids (spec 08 §3.1). An empty cluster is reseeded to
// the worst-fit point so all k centroids are returned. The seed makes the build
// reproducible, which the IVF determinism test depends on (spec 08 §3.5).
func trainCentroids(data []float32, n, dim, k, iters int, seed int64) []float32 {
	if k > n {
		k = n
	}
	rng := rand.New(rand.NewSource(seed))
	centroids := make([]float32, k*dim)
	kppInit(data, n, dim, k, centroids, rng)

	assign := make([]int, n)
	counts := make([]int, k)
	for it := 0; it < iters; it++ {
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
		for c := range counts {
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
				reseed(data, n, dim, centroids, assign, c, rng)
				continue
			}
			inv := 1.0 / float32(counts[c])
			base := c * dim
			for j := 0; j < dim; j++ {
				centroids[base+j] *= inv
			}
		}
		if !changed && it > 0 {
			break
		}
	}
	return centroids
}

// kppInit seeds centroids with the k-means++ rule (spec 08 §3.3): the first is a
// random point, each next is chosen with probability proportional to its squared
// distance to the nearest already-chosen centroid.
func kppInit(data []float32, n, dim, k int, centroids []float32, rng *rand.Rand) {
	first := rng.Intn(n)
	copy(centroids[:dim], data[first*dim:(first+1)*dim])

	dist2 := make([]float32, n)
	for i := range dist2 {
		dist2[i] = math.MaxFloat32
	}
	for c := 1; c < k; c++ {
		prev := centroids[(c-1)*dim : c*dim]
		var total float64
		for i := 0; i < n; i++ {
			d := distance.L2SquaredFloat32(data[i*dim:(i+1)*dim], prev)
			if d < dist2[i] {
				dist2[i] = d
			}
			total += float64(dist2[i])
		}
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

// reseed moves an empty centroid onto the point that fits its own cluster worst,
// so no cluster is wasted (spec 08 §3.4).
func reseed(data []float32, n, dim int, centroids []float32, assign []int, c int, rng *rand.Rand) {
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

// roundPow2 rounds n down to the nearest power of two, at least 1 (spec 08 §5.2).
func roundPow2(n int) int {
	if n < 1 {
		return 1
	}
	p := 1
	for p*2 <= n {
		p *= 2
	}
	return p
}
