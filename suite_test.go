package main

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = BeforeSuite(func() {
})

func TestArgoRolloutsManager(t *testing.T) {
	suiteConfig, _ := GinkgoConfiguration()

	RegisterFailHandler(Fail)

	RunSpecs(t, "Test Suite", suiteConfig)
}

var _ = Describe("generateRunList tests", func() {

	DescribeTable("Single param tests", func(success bool, parameters paramList, combo []int, expectedRes [][]int) {

		combosToRun := generateAllCombos(parameters)

		expectedComboLen := 1
		for _, param := range parameters {
			expectedComboLen *= len(param.values)
		}
		Expect(combosToRun).To(HaveLen(expectedComboLen))

		res := generateRunList(success, combo, combosToRun, parameters)

		Expect(res).To(Equal(expectedRes))

	},
		Entry("larger is better, success: should remove smaller combos",
			true,
			paramList{
				{
					values:   []any{0, 1, 2},
					dataType: dataType_largerIsBetter,
				}}, []int{1},
			[][]int{{2}},
		),
		Entry("larger is better, fail: should remove larger combos",
			false,
			paramList{
				{
					values:   []any{0, 1, 2},
					dataType: dataType_largerIsBetter,
				}}, []int{1},
			[][]int{{0}},
		),
		Entry("smaller is better, success: should remove larger combos",
			true,
			paramList{
				{
					values:   []any{0, 1, 2},
					dataType: dataType_smallerIsBetter,
				}}, []int{1},
			[][]int{{0}},
		),
		Entry("smaller is better, fail: should remove smaller combos",
			false,
			paramList{
				{
					values:   []any{0, 1, 2},
					dataType: dataType_smallerIsBetter,
				}}, []int{1},
			[][]int{{2}},
		),
	)

	DescribeTable("Multi param tests", func(success bool, parameters paramList, combo []int, expectedRes [][]int) {

		combosToRun := generateAllCombos(parameters)

		expectedComboLen := 1
		for _, param := range parameters {
			expectedComboLen *= len(param.values)
		}
		Expect(combosToRun).To(HaveLen(expectedComboLen))

		res := generateRunList(success, combo, combosToRun, parameters)

		Expect(res).To(Equal(expectedRes))

	}, Entry("larger is better, success: should remove combos where both numbers are smaller",
		true,
		paramList{
			{
				values:   []any{0, 1, 2},
				dataType: dataType_largerIsBetter,
			},
			{
				values:   []any{0, 1, 2},
				dataType: dataType_largerIsBetter,
			},
		}, []int{1, 1},
		[][]int{{0, 2}, {1, 2}, {2, 0}, {2, 1}, {2, 2}},
	),
		Entry("larger is better, fail: should remove combos where both numbers are larger",
			false,
			paramList{
				{
					values:   []any{0, 1, 2},
					dataType: dataType_largerIsBetter,
				},
				{
					values:   []any{0, 1, 2},
					dataType: dataType_largerIsBetter,
				},
			}, []int{1, 1},
			[][]int{{0, 0}, {0, 1}, {0, 2}, {1, 0}, {2, 0}},
		),
		Entry("smaller is better, success: should remove combos where both numbers are larger",
			true,
			paramList{
				{
					values:   []any{0, 1, 2},
					dataType: dataType_smallerIsBetter,
				},
				{
					values:   []any{0, 1, 2},
					dataType: dataType_smallerIsBetter,
				},
			}, []int{1, 1},
			[][]int{{0, 0}, {0, 1}, {0, 2}, {1, 0}, {2, 0}},
		),
		Entry("smaller is better, fail: should remove combos where both numbers are larger",
			false,
			paramList{
				{
					values:   []any{0, 1, 2},
					dataType: dataType_smallerIsBetter,
				},
				{
					values:   []any{0, 1, 2},
					dataType: dataType_smallerIsBetter,
				},
			}, []int{1, 1},
			[][]int{{0, 2}, {1, 2}, {2, 0}, {2, 1}, {2, 2}},
		),
		Entry("both types, pass: should process each element of combo based on parameter type",
			true,
			paramList{
				{
					values:   []any{0, 1, 2},
					dataType: dataType_smallerIsBetter,
				},
				{
					values:   []any{0, 1, 2},
					dataType: dataType_largerIsBetter,
				},
			}, []int{1, 1},
			[][]int{{0, 0}, {0, 1}, {0, 2}, {1, 2}, {2, 2}},
			// [][]int{{0, 0}, {0, 1}, {0, 2}, {1, 0}, {1, 1}, {1, 2}, {2, 0}, {2, 1}, {2, 2}},
		),
		Entry("both types, fail: should process each element of combo based on parameter type",
			false,
			paramList{
				{
					values:   []any{0, 1, 2},
					dataType: dataType_smallerIsBetter,
				},
				{
					values:   []any{0, 1, 2},
					dataType: dataType_largerIsBetter,
				},
			}, []int{1, 1},
			[][]int{{0, 0}, {1, 0}, {2, 0}, {2, 1}, {2, 2}},
		),
	)
})
