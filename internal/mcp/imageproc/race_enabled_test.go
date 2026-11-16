//go:build race

package imageproc

// raceEnabled reports whether the race detector is compiled in. Under -race
// sync.Pool randomly drops items, so heap totals do not reflect production.
const raceEnabled = true
