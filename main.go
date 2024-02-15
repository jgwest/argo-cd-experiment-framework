package main

import (
	"context"
	"fmt"
	fatallog "log"
	"slices"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

var (
	resourceUsageDebug = false
)

type parameter struct {
	name     string
	values   []any
	dataType dataType
}

func generateIntegerParameterValues(start int, endInclusive int) []any {
	var res []any
	for ; start <= endInclusive; start++ {
		res = append(res, start)
	}
	return res
}

func generateIntegerParameterValuesWithIncrement(start int, endInclusive int, increment int) []any {
	var res []any
	for ; start <= endInclusive; start += increment {
		res = append(res, start)
	}
	return res
}

func generateAllCombos(params []parameter) [][]int {

	res := [][]int{}

	curr := make([]int, len(params))

	for idx := range curr {
		curr[idx] = 0
	}

	fmt.Println("start")

outer:
	for {

		entry := make([]int, len(params))
		copy(entry, curr)
		res = append(res, entry)

		curr[len(curr)-1]++

		for idx := len(curr) - 1; idx >= 0; idx-- {

			if curr[idx] == len(params[idx].values) {
				curr[idx] = 0

				if idx-1 < 0 {
					break outer
				}

				curr[idx-1]++
			}
		}

	}

	return res

}

type paramList []parameter

func (paramList paramList) findParam(name string) *parameter {

	for idx := range paramList {
		if paramList[idx].name == name {
			return &paramList[idx]
		}
	}

	return nil
}

type dataType int

const (
	// dataType_largerIsBetter:
	// if 5 passes, no reason to test 2, 3, 4
	// if 2 fails, no reason to test 3, 4, 5
	// on fail, skip anything larger
	// on success, skip anything smaller
	dataType_largerIsBetter dataType = 1

	// dataType_smallerIsBetter:
	// if 500 passes, no reason to test 1000, 1500, etc
	// if 3000 fails, no reason to test 2500, 2000, etc.
	// on fail, skip anything smaller, mark as 'expected fail'
	// on success, skip anything larger, mark as 'expected pass'
	dataType_smallerIsBetter dataType = 2
)

func main() {

	ctx := context.Background()

	kLog := log.FromContext(ctx)

	c, err := createMyClient()
	if err != nil {
		fatallog.Fatal(err)
	}

	parameters := paramList{{
		name:     "processors",
		values:   generateIntegerParameterValuesWithIncrement(2, 17, 5),
		dataType: dataType_largerIsBetter,
		// for example, with a data set of 2,3,4,5, representing number of application controller workers
		// if 5 passes, no reason to test 2, 3, 4
		// if 2 fails, no reason to test 3, 4, 5
		// on fail, skip anything larger
		// on success, skip anything smaller
	}, {
		name:     "application-controller-memory",
		values:   generateIntegerParameterValuesWithIncrement(500, 5000, 500),
		dataType: dataType_smallerIsBetter,
		// for example, with a data set of 500, 1000, 1500, 2000, 2500, 3000, representing memory usage of application controller
		// if 500 passes, no reason to test 1000, 1500, etc
		// if 3000 fails, no reason to test 2500, 2000, etc.
		// on fail, skip anything smaller, mark as 'expected fail'
		// on success, skip anything larger, mark as 'expected pass'
	}}

	allCombos := generateAllCombos(parameters)

	combosToRun := make([][]int, len(allCombos))
	copy(combosToRun, allCombos)

	appsToTest := 90

	for len(combosToRun) > 0 {

		outputStatus(fmt.Sprintf("Status: %d combos left to run.", len(combosToRun)))
		outputStatus()

		combo := findNextComboToRun(combosToRun, len(parameters))

		idx, found := findComboIndexInComboList(combosToRun, combo)
		if !found {
			fatallog.Fatal("unable to locate next combo to run in list: this should never happen.")
			return
		}

		combosToRun = append(combosToRun[0:idx], combosToRun[idx+1:]...)

		processors := (parameters.findParam("processors").values[combo[0]]).(int)

		appControllerMemory := (parameters.findParam("application-controller-memory").values[combo[1]]).(int)

		outputStatus("Running:", coordinateString(combo, parameters))

		experiment := createExperiment_largeApps(appsToTest, &applicationControllerSettings{operationProcessors: processors, statusProcessors: processors, kubectlParallelismLimit: processors, resourceRequirements: createResourceRequirements("250m", "250Mi", "2", fmt.Sprintf("%dMi", appControllerMemory))}, 1)

		success, err := runExperimentXTimes(ctx, experiment, c, kLog)

		outputStatus("Result:", coordinateString(combo, parameters), success, err)

		combosToRun = generateRunList(success, combo, combosToRun, parameters)

	}
}

func sortByValCopy(combosToRun [][]int, paramToSortBy int) [][]int {
	res := make([][]int, len(combosToRun))
	copy(res, combosToRun)

	slices.SortFunc(res, func(a, b []int) int {
		return a[paramToSortBy] - b[paramToSortBy]
	})

	return res
}

func filterByValCopy(combosToRun [][]int, paramToFilterBy int, requiredValue int) [][]int {

	res := [][]int{}

	for idx := range combosToRun {

		curr := combosToRun[idx]
		if curr[paramToFilterBy] == requiredValue {
			res = append(res, curr)
		}
	}

	return res

}

func findComboIndexInComboList(combosToRun [][]int, comboToFind []int) (int, bool) {

outer:
	for idx := range combosToRun {
		currCombo := combosToRun[idx]

		for currComboIdx := range currCombo {

			if currCombo[currComboIdx] != comboToFind[currComboIdx] {
				continue outer
			}

		}
		return idx, true

	}

	return -1, false
}

func findNextComboToRun(combosToRun [][]int, paramListSize int) []int {

	candidateCombosToRun := make([][]int, len(combosToRun))
	copy(candidateCombosToRun, combosToRun)

	for x := 0; x < paramListSize; x++ {

		candidateCombosToRun = sortByValCopy(candidateCombosToRun, x)

		middleValueForParam := candidateCombosToRun[len(candidateCombosToRun)/2]

		candidateCombosToRun = filterByValCopy(candidateCombosToRun, x, middleValueForParam[x])
	}

	return candidateCombosToRun[0]
}

func dateTimeString() string {
	t := time.Now()
	dateText := t.Format(time.RFC3339)

	return dateText
}

func outputStatus(str ...any) {

	fmt.Print(dateTimeString(), " | ")
	fmt.Println(str...)

}

func coordinateString(combo []int, parameters paramList) string {
	var resultLine string

	for idx := range combo {
		param := parameters[idx]
		resultLine += fmt.Sprintf("%s: %v", param.name, param.values[combo[idx]])
		resultLine += "   "
	}

	return resultLine

}

func generateRunList(success bool, combo []int, combosToRun [][]int, parameters paramList) [][]int {
	var newCombosToRun [][]int

	for idx := range combosToRun {

		combosToRunEntry := combosToRun[idx]

		skipCount := 0

		for comboIdx := range combosToRunEntry {
			param := parameters[comboIdx]

			if success {

				if param.dataType == dataType_largerIsBetter {
					// on success, skip anything smaller

					if combo[comboIdx] >= combosToRunEntry[comboIdx] {
						skipCount++
					}

				} else if param.dataType == dataType_smallerIsBetter {
					// on success, skip anything larger

					if combo[comboIdx] <= combosToRunEntry[comboIdx] {
						skipCount++
					}
				}

			} else {

				if param.dataType == dataType_largerIsBetter {
					// on fail, skip anything larger

					if combo[comboIdx] <= combosToRunEntry[comboIdx] {
						skipCount++
					}

				} else if param.dataType == dataType_smallerIsBetter {
					// on fail, skip anything smaller

					if combo[comboIdx] >= combosToRunEntry[comboIdx] {
						skipCount++
					}
				}

			}

		}

		if skipCount == len(parameters) {
			// report entry as skipped, either pass or fail

			status := ""
			if success {
				status = "expected-to-pass"
			} else {
				status = "expected-to-fail"
			}

			fmt.Println("Skipping", coordinateString(combosToRunEntry, parameters), status)

		} else {
			newCombosToRun = append(newCombosToRun, combosToRunEntry)
		}

	}

	return newCombosToRun
}

func main2() {

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
