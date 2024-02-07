package main

import (
	"context"
	"fmt"
	fatallog "log"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

var (
	resourceUsageDebug = false
)

func main() {

	ctx := context.Background()

	kLog := log.FromContext(ctx)

	c, err := createMyClient()
	if err != nil {
		fatallog.Fatal(err)
	}

	if err := findMemoryValue(ctx, 20, c, kLog); err != nil {
		fatallog.Fatal(err)
	} else {
		return
	}

	{

		// resources := createResourceRequirements("250m", "250Mi", "2", "2Gi")

		experimentsToRun := []experiment{

			// createExperiment_largeApps(20, &applicationControllerSettings{operationProcessors: 5, statusProcessors: 5, kubectlParallelismLimit: 5, resourceRequirements: createResourceRequirements("250m", "250Mi", "2", "500Mi")}, 3),

			// createExperiment_largeApps(10, &applicationControllerSettings{operationProcessors: 2, statusProcessors: 2, kubectlParallelismLimit: 2, resourceRequirements: createResourceRequirements("250m", "250Mi", "2", "500Mi")}, 2),

			// createExperiment_largeApps(10, &applicationControllerSettings{operationProcessors: 3, statusProcessors: 3, kubectlParallelismLimit: 3, resourceRequirements: createResourceRequirements("250m", "250Mi", "2", "900Mi")}, 2),

			// createExperiment_largeApps(10, &applicationControllerSettings{operationProcessors: 3, statusProcessors: 3, kubectlParallelismLimit: 4, resourceRequirements: createResourceRequirements("250m", "250Mi", "2", "900Mi")}, 2),

			// createExperiment_largeApps(10, &applicationControllerSettings{operationProcessors: 3, statusProcessors: 3, kubectlParallelismLimit: 6, resourceRequirements: createResourceRequirements("250m", "250Mi", "2", "900Mi")}, 2),

			// createExperiment_largeApps(10, &applicationControllerSettings{operationProcessors: 3, statusProcessors: 3, kubectlParallelismLimit: 8, resourceRequirements: createResourceRequirements("250m", "250Mi", "2", "900Mi")}, 2),

			// createExperiment_largeApps(10, &applicationControllerSettings{operationProcessors: 3, statusProcessors: 3, kubectlParallelismLimit: 10, resourceRequirements: createResourceRequirements("250m", "250Mi", "2", "900Mi")}, 2),

			// createExperiment_largeApps(10, &applicationControllerSettings{operationProcessors: 3, statusProcessors: 3, kubectlParallelismLimit: 3, resourceRequirements: createResourceRequirements("250m", "250Mi", "2", "700Mi")}, 2),

			// createExperiment_largeApps(10, &applicationControllerSettings{operationProcessors: 4, statusProcessors: 4, kubectlParallelismLimit: 4, resourceRequirements: createResourceRequirements("250m", "250Mi", "2", "700Mi")}, 2),

			// createExperiment_largeApps(20, &applicationControllerSettings{operationProcessors: 5, statusProcessors: 5, kubectlParallelismLimit: 5, resourceRequirements: createResourceRequirements("250m", "250Mi", "2", "900Mi")}, 3),

			// createExperiment_largeApps(20, &applicationControllerSettings{operationProcessors: 5, statusProcessors: 5, kubectlParallelismLimit: 5, resourceRequirements: createResourceRequirements("250m", "250Mi", "2", "1100Mi")}, 3),

			// createExperiment_mediumApps(),
			// createExperiment_216KiBConfigMap(),
			// createExperiment_unmanagedConfigMaps(),
		}

		for _, experiment := range experimentsToRun {

			_, _ = runExperimentXTimes(ctx, experiment, c, kLog)

			// 	numberOfRuns := 1

			// 	if experiment.runXTimes > 0 {
			// 		numberOfRuns = experiment.runXTimes
			// 	}

			// 	for runNumber := 0; runNumber < numberOfRuns; runNumber++ {

			// 		testName := experiment.name

			// 		if numberOfRuns > 1 {
			// 			testName = fmt.Sprintf("%s (#%d of %d)", testName, runNumber+1, numberOfRuns)
			// 		}

			// 		actionOutput("------------------------------ " + testName + " -------------------------------------")

			// 		success, err := runExperiment(ctx, experiment, c, kLog)
			// 		if err != nil {
			// 			actionOutput(fmt.Sprintf("Error occurred, ending experiment run: %v", err))
			// 			break
			// 		}

			// 		if !success {
			// 			actionOutput(fmt.Sprintf("Run %d failed, ending experiment run.", runNumber+1))
			// 			break
			// 		}
			// 	}

		}
	}
}

func runExperimentXTimes(ctx context.Context, experiment experiment, c *myClient, kLog logr.Logger) (bool, error) {
	numberOfRuns := 1

	if experiment.runXTimes > 0 {
		numberOfRuns = experiment.runXTimes
	}

	for runNumber := 0; runNumber < numberOfRuns; runNumber++ {

		testName := experiment.name

		if numberOfRuns > 1 {
			testName = fmt.Sprintf("%s (#%d of %d)", testName, runNumber+1, numberOfRuns)
		}

		actionOutput("------------------------------ " + testName + " -------------------------------------")

		success, err := runExperiment(ctx, experiment, c, kLog)
		if err != nil {
			actionOutput(fmt.Sprintf("Error occurred, ending experiment run: %v", err))
			return false, err
		}

		if !success {
			actionOutput(fmt.Sprintf("Run %d failed, ending experiment run.", runNumber+1))
			return false, err
		}
	}

	return true, nil

}

func runExperiment(ctx context.Context, experiment experiment, myClient *myClient, kLog logr.Logger) (bool, error) {

	actionOutput("Pre: Delete previous test resources")
	if err := deleteOldAnnotatedResources(ctx, myClient); err != nil {
		return false, err
	}

	actionOutput("Pre: Setup environment")

	if experiment.appControllerSettings != nil {
		actionOutput("Application Controller settings:")
		actionOutput(fmt.Sprintf("- operationProcessors: %d", experiment.appControllerSettings.operationProcessors))
		actionOutput(fmt.Sprintf("- statusProcessors: %d", experiment.appControllerSettings.statusProcessors))
		actionOutput(fmt.Sprintf("- kubectlParallelismLimit: %d", experiment.appControllerSettings.kubectlParallelismLimit))

		if experiment.appControllerSettings.resourceRequirements != nil {
			actionOutput(fmt.Sprintf("- resources: %v", experiment.appControllerSettings.resourceRequirements))
		}

	} else {
		actionOutput("Application Controller settings: default")
	}

	if err := initialConfiguration(ctx, experiment.appControllerSettings, myClient, kLog); err != nil {
		return false, err
	}

	success, err := beginExperiment(ctx, experiment, myClient, kLog)

	actionOutput("Post: Cleaning up old resources")
	myClient.ledger.disposeAll(ctx, myClient)

	return success, err

}

func findMemoryValue(ctx context.Context, appsToTest int, c *myClient, kLog logr.Logger) error {

	processors := 3

	startVal := 1000

	// Find the first value that successfully completes the experiment. This will be the upper bound of the final value.
	firstSuccess := 0
	{
		currVal := startVal
		for {

			experiment := createExperiment_largeApps(appsToTest, &applicationControllerSettings{operationProcessors: processors, statusProcessors: processors, kubectlParallelismLimit: processors, resourceRequirements: createResourceRequirements("250m", "250Mi", "2", fmt.Sprintf("%dMi", currVal))}, 2)

			success, err := runExperimentXTimes(ctx, experiment, c, kLog)
			if err != nil {
				return err
			}

			if success {
				actionOutput("First successful experiment found.")
				firstSuccess = currVal
				break

			} else {
				startVal = currVal
				actionOutput("Experiment failed, increasing the value")
				currVal = (int)(float32(currVal) * 1.5)
			}

		}
	}

	var allValuesToTest []experiment

	for x := startVal; x <= firstSuccess; x += 100 {

		experiment := createExperiment_largeApps(appsToTest, &applicationControllerSettings{operationProcessors: processors, statusProcessors: processors, kubectlParallelismLimit: processors, resourceRequirements: createResourceRequirements("250m", "250Mi", "2", fmt.Sprintf("%dMi", x))}, 2)

		allValuesToTest = append(allValuesToTest, experiment)
	}

	lowerRange := 0
	upperRange := len(allValuesToTest) - 1

	for {

		if lowerRange > upperRange {
			break
		}

		currPos := (lowerRange + upperRange) / 2

		fmt.Println(lowerRange, upperRange, currPos)

		experiment := allValuesToTest[currPos]

		success, err := runExperimentXTimes(ctx, experiment, c, kLog)
		if err != nil {
			return err
		}

		if success {
			// On success, search the lower search space
			upperRange = currPos - 1
		} else {
			// On failure, search the upper search space
			lowerRange = currPos + 1
		}
	}

	return nil

}
