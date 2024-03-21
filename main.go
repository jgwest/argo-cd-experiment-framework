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

const (
	// dataType_largerIsBetter:
	// for example, with a data set of 2,3,4,5, representing number of application controller workers
	// if 5 passes, no reason to test 2, 3, 4
	// if 2 fails, no reason to test 3, 4, 5
	// on fail, skip anything larger
	// on success, skip anything smaller
	dataType_largerIsBetter dataType = 1

	// dataType_smallerIsBetter:
	// for example, with a data set of 500, 1000, 1500, 2000, 2500, 3000, representing memory usage of application controller
	// if 500 passes, no reason to test 1000, 1500, etc
	// if 3000 fails, no reason to test 2500, 2000, etc.
	// on fail, skip anything smaller, mark as 'expected fail'
	// on success, skip anything larger, mark as 'expected pass'
	dataType_smallerIsBetter dataType = 2
)

func main() {

	// parameters := paramList{{
	// 	name:     "processors",
	// 	values:   generateIntegerParameterValuesWithIncrement(2, 17, 5),
	// 	dataType: dataType_largerIsBetter,
	// }, {
	// 	name:     "application-controller-memory",
	// 	values:   generateIntegerParameterValuesWithIncrement(1500, 4500, 500),
	// 	dataType: dataType_smallerIsBetter,
	// }}

	parameters := paramList{{
		name:     "applications",
		values:   generateIntegerParameterValuesWithIncrement(40, 120, 20),
		dataType: dataType_largerIsBetter,
		applyValueFn: func(val int, paramsToModify *experimentExecutionParams) {
			paramsToModify.appsToTest = val
		},
	}, {
		name:     "application-controller-memory",
		values:   generateIntegerParameterValuesWithIncrement(1000, 5000, 500),
		dataType: dataType_smallerIsBetter,
		applyValueFn: func(val int, paramsToModify *experimentExecutionParams) {
			paramsToModify.appControllerSettings.resourceRequirements = createResourceRequirements("250m", "250Mi", "2", fmt.Sprintf("%dMi", val))
		},
	}}

	runWithParameters(parameters)
}

type experimentExecutionParams struct {
	appsToTest            int
	appControllerSettings applicationControllerSettings
	runXTimes             int
}

func runWithParameters(parameters paramList) {
	ctx := context.Background()

	kLog := log.FromContext(ctx)

	c, err := createExperimentClient()
	if err != nil {
		fatallog.Fatal(err)
	}

	var results resultsList

	allCombos := generateAllCombos(parameters)

	combosToRun := make([][]int, len(allCombos))
	copy(combosToRun, allCombos)

	defaultAppsToTest := 100
	defaultProcessors := 10
	defaultAppControllerMemLimit := 1000
	defaultRunXTimes := 1

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

		// processors := (parameters.findParam("processors").values[combo[0]]).(int)

		// appControllerMemory := (parameters.findParam("application-controller-memory").values[combo[1]]).(int)

		outputStatus("Running:", coordinateString(combo, parameters))

		expParams := experimentExecutionParams{
			appsToTest:            defaultAppsToTest,
			appControllerSettings: applicationControllerSettings{operationProcessors: defaultProcessors, statusProcessors: defaultProcessors, kubectlParallelismLimit: defaultProcessors, resourceRequirements: createResourceRequirements("250m", "250Mi", "2", fmt.Sprintf("%dMi", defaultAppControllerMemLimit))},
			runXTimes:             defaultRunXTimes,
		}

		for paramIndex := range parameters {
			currParam := parameters[paramIndex]

			valueToSet := currParam.values[combo[paramIndex]]

			fmt.Println("[hi] set "+currParam.name+" to", valueToSet)
			currParam.applyValueFn((valueToSet).(int), &expParams)
		}

		experiment := createExperiment_largeApps(expParams)

		success, err := runExperimentXTimes(ctx, experiment, c, kLog)

		{
			statusName := "passed"
			if !success {
				statusName = "failed" // TODO: Convert to constants
			}
			results.addResult(resultEntry{
				combo:   combo,
				success: success,
				err:     err,
				status:  statusName,
			})

		}
		outputStatus("Result:", coordinateString(combo, parameters), success, err)

		combosToRun = generateRunList(success, combo, combosToRun, parameters, &results)

	}

	outputStatus("All results:")

	for _, resultEntry := range results.entries {
		outputStatus(coordinateString(resultEntry.combo, parameters), resultEntry.success, resultEntry.status, resultEntry.err)
	}

	generate2DMarkdownTable(parameters, results)
}

func generate2DMarkdownTable(parameters paramList, results resultsList) {

	// Initialize cells
	cells := make([][]string, len(parameters[0].values)+1)
	for i := range cells {
		cells[i] = make([]string, len(parameters[1].values)+1)
	}

	// Add data
	for _, resultEntry := range results.entries {
		x := resultEntry.combo[0]
		y := resultEntry.combo[1]

		cells[x+1][y+1] = coordinateString(resultEntry.combo, parameters) + "/" + resultEntry.status
	}

	// Add x header
	for x := 0; x < len(parameters[0].values); x++ {
		cells[x+1][0] = fmt.Sprintf("%s %d", parameters[0].name, (parameters[0].values[x]).(int))
	}

	// Add y header
	for y := 0; y < len(parameters[1].values); y++ {
		cells[0][y+1] = fmt.Sprintf("%s %d", parameters[1].name, (parameters[1].values[y]).(int))
	}

	for y := 0; y < len(parameters[1].values)+1; y++ {

		// Print header required for Markdown tables
		if y == 1 {
			fmt.Print("|")
			for x := 0; x < len(parameters[0].values)+1; x++ {
				fmt.Print("---|")
			}
			fmt.Println()
		}

		fmt.Print("|")
		for x := 0; x < len(parameters[0].values)+1; x++ {
			fmt.Print(" " + cells[x][y] + " |")
		}
		fmt.Println()
	}

}

// sortByValCopy returned a copy of combosToRun that has been sorted by parameter 'paramToSortBy', in ascdending order
func sortByValCopy(combosToRun [][]int, paramToSortBy int) [][]int {
	res := make([][]int, len(combosToRun))
	copy(res, combosToRun)

	slices.SortFunc(res, func(a, b []int) int {
		return a[paramToSortBy] - b[paramToSortBy]
	})

	return res
}

// filterByValCopy returns a copy of combosToRun, where the copy only contains entries where parameter index of 'paramToFilterBy' has value 'requiredValue'.
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

// findComboIndexInComboList returns the index of 'comboToFind' within 'combosToRun', or 'false' if not found.
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

func generateRunList(success bool, combo []int, combosToRun [][]int, parameters paramList, results *resultsList) [][]int {
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
				status = "expected-to-pass" // TODO: convert to constants
			} else {
				status = "expected-to-fail"
			}

			results.addResult(resultEntry{
				combo:   combosToRunEntry,
				success: success,
				err:     nil,
				status:  status,
			})

			outputStatus("Skipping", coordinateString(combosToRunEntry, parameters), status)

		} else {
			newCombosToRun = append(newCombosToRun, combosToRunEntry)
		}

	}

	return newCombosToRun
}

func main2() {

	ctx := context.Background()

	kLog := log.FromContext(ctx)

	c, err := createExperimentClient()
	if err != nil {
		fatallog.Fatal(err)
	}

	if err := findMemoryValue(ctx, 20, c, kLog); err != nil {
		fatallog.Fatal(err)
	} else {
		return
	}

	{

		experimentsToRun := []experiment{

			// createExperiment_largeApps(20, &applicationControllerSettings{operationProcessors: 5, statusProcessors: 5, kubectlParallelismLimit: 5, resourceRequirements: createResourceRequirements("250m", "250Mi", "2", "500Mi")}, 3),

			// createExperiment_mediumApps(),
			// createExperiment_216KiBConfigMap(),
			// createExperiment_unmanagedConfigMaps(),
		}

		for _, experiment := range experimentsToRun {

			_, _ = runExperimentXTimes(ctx, experiment, c, kLog)

		}
	}
}

func runExperimentXTimes(ctx context.Context, experiment experiment, c *experimentClient, kLog logr.Logger) (bool, error) {
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

func runExperiment(ctx context.Context, experiment experiment, expClient *experimentClient, kLog logr.Logger) (bool, error) {

	actionOutput("Pre: Delete previous test resources")
	if err := deleteOldAnnotatedResources(ctx, expClient); err != nil {
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

	if err := initialConfiguration(ctx, argoCDInstallType_OpenShift, experiment.appControllerSettings, expClient, kLog); err != nil {
		return false, err
	}

	success, err := beginExperiment(ctx, experiment, expClient, kLog)

	actionOutput("Post: Cleaning up old resources")
	expClient.ledger.disposeAll(ctx, expClient)

	return success, err

}

func findMemoryValue(ctx context.Context, appsToTest int, c *experimentClient, kLog logr.Logger) error {

	processors := 3

	startVal := 1000

	// Find the first value that successfully completes the experiment. This will be the upper bound of the final value.
	firstSuccess := 0
	{
		currVal := startVal
		for {

			expParams := experimentExecutionParams{
				appsToTest:            appsToTest,
				appControllerSettings: applicationControllerSettings{operationProcessors: processors, statusProcessors: processors, kubectlParallelismLimit: processors, resourceRequirements: createResourceRequirements("250m", "250Mi", "2", fmt.Sprintf("%dMi", currVal))},
				runXTimes:             2,
			}

			experiment := createExperiment_largeApps(expParams)

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

		expParams := experimentExecutionParams{
			appsToTest:            appsToTest,
			appControllerSettings: applicationControllerSettings{operationProcessors: processors, statusProcessors: processors, kubectlParallelismLimit: processors, resourceRequirements: createResourceRequirements("250m", "250Mi", "2", fmt.Sprintf("%dMi", x))},
			runXTimes:             2,
		}

		experiment := createExperiment_largeApps(expParams)

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

type parameter struct {
	name         string
	values       []any
	dataType     dataType
	applyValueFn func(val int, paramsToModify *experimentExecutionParams)
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

type resultEntry struct {
	combo   []int
	success bool
	err     error
	status  string
	values  map[string]any
}

type resultsList struct {
	entries []resultEntry
}

func (r *resultsList) addResult(entry resultEntry) {
	r.entries = append(r.entries, entry)
}
