package main

import (
	"context"
	"fmt"
	fatallog "log"
	"sync"
	"time"

	"github.com/go-logr/logr"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

var (
	debug = false
)

func main() {

	ctx := context.Background()

	kLog := log.FromContext(ctx)

	myClient, err := createMyClient()
	if err != nil {
		fatallog.Fatal(err)
	}

	// TODO: Assert no change in number of OOMs

	{

		experimentsToRun := []experiment{
			createExperiment_216KiBConfigMap(),
			createExperiment_unmanagedConfigMaps(),
		}

		for _, experiment := range experimentsToRun {

			actionOutput("------------------------------ " + experiment.name + " -------------------------------------")

			actionOutput("Pre: Setup environment")
			if err := initialConfiguration(ctx, myClient, kLog); err != nil {
				fatallog.Fatal(err)
			}

			if err := beginExperiment(ctx, experiment, myClient, kLog); err != nil {
				fatallog.Fatal(err)
			}

			actionOutput("Post: Cleaning up old resources")
			myClient.ledger.disposeAll(ctx, myClient)

		}

	}

	// if err := initialConfiguration(ctx, myClient, kLog); err != nil {
	// 	fatallog.Fatal(err)
	// }

	// if result, err := experiment_unmanagedConfigMaps(ctx, myClient, kLog); err != nil {
	// 	fatallog.Fatal(err)
	// } else {
	// 	if result.success {
	// 		fmt.Println("SUCCESS")
	// 	} else {
	// 		fmt.Println("FAIL")
	// 	}
	// }

	// // if result, err := experiment_216KiBConfigMap(ctx, myClient, kLog); err != nil {
	// // 	fatallog.Fatal(err)
	// // } else {
	// // 	if result.success {
	// // 		fmt.Println("SUCCESS")
	// // 	} else {
	// // 		fmt.Println("FAIL")
	// // 	}
	// // }
	// actionOutput("Cleaning up old resources")
	// myClient.ledger.disposeAll(ctx, myClient)

	// // if err := generateRepo("jgw", "jgw", kClient); err != nil {
	// // 	fmt.Println(err)
	// // 	return
	// // }

}

type experiment struct {
	name string
	fn   func(ctx context.Context, myClient *myClient, kLog logr.Logger) (*experimentResult, error)
}

func beginExperiment(ctx context.Context, e experiment, myClient *myClient, kLog logr.Logger) error {

	fmt.Println()
	actionOutput("Beginning experiment: " + e.name)
	result, err := e.fn(ctx, myClient, kLog)
	if err != nil {
		return err
	}

	if result.success {
		fmt.Println("SUCCESS")
	} else {
		fmt.Println("FAIL")
	}
	actionOutput("Experiment complete")
	fmt.Println()

	return nil
}

type runnableTask interface {
	runTask(ctx context.Context, taskNumber int, client *myClient) error
}

func runTasks(ctx context.Context, maxConcurrentTasks int, availableWork []runnableTask, myClient *myClient) error {

	concurrentTasks := 0

	totalWork := len(availableWork)

	var mutex sync.Mutex

	var nextTaskNumber int

	nextPercentToReport := 95

	for {

		workAdded := false

		mutex.Lock()
		{

			percentRemaining := int(float32(100) * float32(len(availableWork)) / float32(totalWork))

			if percentRemaining > 0 && percentRemaining <= nextPercentToReport {
				fmt.Printf("- Tasks remaining: %v%%, %v/%v\n", percentRemaining, len(availableWork), totalWork)
				nextPercentToReport -= 5
			}

			if len(availableWork) == 0 && concurrentTasks == 0 {
				break
			}

			if concurrentTasks < maxConcurrentTasks && len(availableWork) > 0 {

				concurrentTasks++
				localNextTaskNumber := nextTaskNumber
				nextTaskNumber++

				work := availableWork[0]
				availableWork = availableWork[1:]

				workAdded = true
				go func(taskNumber int) {

					work.runTask(ctx, taskNumber, myClient)

					mutex.Lock()
					concurrentTasks--
					mutex.Unlock()
				}(localNextTaskNumber)
			}
		}
		mutex.Unlock()

		if !workAdded {
			time.Sleep(100 * time.Millisecond)
		}

	}

	return nil

}
