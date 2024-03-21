package main

import (
	"context"

	"github.com/go-logr/logr"
)

func createExperiment_sampleTemplate() experiment {
	fn := func(ctx context.Context, expClient *experimentClient, cancelSignalled chan string, kLog logr.Logger) (*experimentResult, error) {

		baseMemoryUsage, err := collectBaseMemoryUsage()
		if err != nil {
			return nil, err
		}

		totalTasks := 250

		deleteOldResources := func() error {
			var availableWork []runnableTask

			for x := 0; x < totalTasks; x++ {
				availableWork = append(availableWork, &workTask_experimentSampleTemplate{})
			}

			actionOutput("...")
			if _, err := runTasksConcurrently(ctx, 30, availableWork, nil, expClient); err != nil {
				return err
			}

			return nil
		}

		cancelled := false
		{
			var availableWork []runnableTask
			for x := 0; x < totalTasks; x++ {
				availableWork = append(availableWork, &workTask_experimentSampleTemplate{})
			}

			actionOutput("...")
			var err error
			if cancelled, err = runTasksConcurrently(ctx, 5, availableWork, cancelSignalled, expClient); err != nil {
				return nil, err
			}
		}

		var success bool
		if !cancelled {

			{
				lowest, highest, avg, err := collectPostMemoryUsage()
				if err != nil {
					return nil, err
				}

				// adjust values to remove the base memory usage
				lowest = lowest - baseMemoryUsage
				highest = highest - baseMemoryUsage
				avg = avg - baseMemoryUsage

				outputStatus("")
				outputStatus("Base memory usage:", baseMemoryUsage)
				outputStatus("Lowest memory usage (3 mins, adjusted):", lowest)
				outputStatus("Highest memory usage (3 mins, adjusted):", highest)
				outputStatus("Avg memory usage (3 mins, adjusted):", avg)

				success = highest > 900 && highest <= 1300
			}
		}

		actionOutput("Cleaning up experiment resources")

		if err := deleteOldResources(); err != nil {
			return nil, err
		}

		return &experimentResult{
			success: success,
		}, nil
	}

	return experiment{
		name: "Sample Template",
		fn:   fn,
	}
}

var _ runnableTask = &workTask_experimentSampleTemplate{}

type workTask_experimentSampleTemplate struct {
}

func (wt *workTask_experimentSampleTemplate) runTask(ctx context.Context, taskNumber int, c *experimentClient) error {

	return nil
}
