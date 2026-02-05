## 2026-02-05 - [Post Filtering and MongoDB Counting Optimization]
**Learning:**
1. Using `[]rune(s)` for string length or truncation in Go allocates a new slice, which is O(N) in both time and space. For large strings in a loop, this is a major bottleneck. Iterating with `range s` is O(N) time but O(1) space.
2. MongoDB's `CountDocuments` with an empty filter is significantly slower than `EstimatedDocumentCount` on large collections because it performs a scan instead of using metadata.
3. Instantiating closures/filter functions inside a loop causes unnecessary allocations.

**Action:**
1. Use a non-allocating rune-aware truncation function.
2. Use `EstimatedDocumentCount` for total collection counts.
3. Move filter generator calls outside of iteration loops.
