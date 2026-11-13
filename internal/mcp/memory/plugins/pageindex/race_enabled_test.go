//go:build race

package pageindex

// raceEnabled reports that the race detector instruments this test binary, which
// slows goldmark several-fold and makes wall-clock comparisons meaningless.
const raceEnabled = true
