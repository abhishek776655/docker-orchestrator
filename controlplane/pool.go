package controlplane

import (
	"errors"
	"os"
	"strconv"
	"sync"
)

// defaultDeployParallelism bounds how many directory snapshots, data seeds
// and builds the control plane packages at once during a deploy. The work is
// disk and CPU bound and every tarball sits in memory, so a small number
// shortens a big deploy without a large memory peak.
const defaultDeployParallelism = 4

func deployParallelism() int {
	if n, err := strconv.Atoi(os.Getenv("HACO_DEPLOY_PARALLELISM")); err == nil && n > 0 {
		return n
	}
	return defaultDeployParallelism
}

// runBounded runs jobs with at most n at a time and waits for all of them.
// Every job runs even if another fails, so one bad service doesn't hide the
// others' problems; the errors come back joined in job order.
func runBounded(n int, jobs []func() error) error {
	if len(jobs) == 0 {
		return nil
	}
	if n < 1 {
		n = 1
	}
	errs := make([]error, len(jobs))
	sem := make(chan struct{}, n)
	var wg sync.WaitGroup
	for i, job := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			errs[i] = job()
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}
