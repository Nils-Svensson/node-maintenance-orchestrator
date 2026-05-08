// drain.go implements node draining behavior.
//
// It wraps Kubernetes eviction logic (via drain.Helper or equivalent)
// and applies user-defined DrainOptions such as concurrency limits,
// force deletion, and handling of specific pod types.
//
// This layer is responsible for executing the actual drain operation,
// but does not decide when or why draining should occur.

package maintenance