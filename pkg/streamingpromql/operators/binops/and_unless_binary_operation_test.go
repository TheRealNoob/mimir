// SPDX-License-Identifier: AGPL-3.0-only

package binops

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"
	"github.com/prometheus/prometheus/promql/parser/posrange"
	"github.com/stretchr/testify/require"

	"github.com/grafana/mimir/pkg/streamingpromql/limiting"
	"github.com/grafana/mimir/pkg/streamingpromql/operators"
	"github.com/grafana/mimir/pkg/streamingpromql/testutils"
	"github.com/grafana/mimir/pkg/streamingpromql/types"
)

func TestAndUnlessBinaryOperation_ClosesInnerOperatorsAsSoonAsPossible(t *testing.T) {
	testCases := map[string]struct {
		isUnless    bool
		leftSeries  []labels.Labels
		rightSeries []labels.Labels

		expectedOutputSeries                        []labels.Labels
		expectLeftSideClosedAfterOutputSeriesIndex  int
		expectRightSideClosedAfterOutputSeriesIndex int
	}{
		"and: reach end of both sides at the same time": {
			isUnless: false,
			leftSeries: []labels.Labels{
				labels.FromStrings("group", "1", "series", "left-1"),
				labels.FromStrings("group", "1", "series", "left-2"),
				labels.FromStrings("group", "2", "series", "left-3"),
			},
			rightSeries: []labels.Labels{
				labels.FromStrings("group", "1", "series", "right-1"),
				labels.FromStrings("group", "1", "series", "right-2"),
				labels.FromStrings("group", "2", "series", "right-3"),
			},

			expectedOutputSeries: []labels.Labels{
				labels.FromStrings("group", "1", "series", "left-1"),
				labels.FromStrings("group", "1", "series", "left-2"),
				labels.FromStrings("group", "2", "series", "left-3"),
			},
			expectLeftSideClosedAfterOutputSeriesIndex:  2,
			expectRightSideClosedAfterOutputSeriesIndex: 2,
		},
		"unless: reach end of both sides at the same time": {
			isUnless: true,
			leftSeries: []labels.Labels{
				labels.FromStrings("group", "1", "series", "left-1"),
				labels.FromStrings("group", "1", "series", "left-2"),
				labels.FromStrings("group", "2", "series", "left-3"),
			},
			rightSeries: []labels.Labels{
				labels.FromStrings("group", "1", "series", "right-1"),
				labels.FromStrings("group", "1", "series", "right-2"),
				labels.FromStrings("group", "2", "series", "right-3"),
			},

			expectedOutputSeries: []labels.Labels{
				labels.FromStrings("group", "1", "series", "left-1"),
				labels.FromStrings("group", "1", "series", "left-2"),
				labels.FromStrings("group", "2", "series", "left-3"),
			},
			expectLeftSideClosedAfterOutputSeriesIndex:  2,
			expectRightSideClosedAfterOutputSeriesIndex: 2,
		},
		"and: no more matches with unmatched series still to read on both sides": {
			isUnless: false,
			leftSeries: []labels.Labels{
				labels.FromStrings("group", "1", "series", "left-1"),
				labels.FromStrings("group", "1", "series", "left-2"),
				labels.FromStrings("group", "2", "series", "left-3"),
			},
			rightSeries: []labels.Labels{
				labels.FromStrings("group", "1", "series", "right-1"),
				labels.FromStrings("group", "1", "series", "right-2"),
				labels.FromStrings("group", "3", "series", "right-3"),
			},

			expectedOutputSeries: []labels.Labels{
				labels.FromStrings("group", "1", "series", "left-1"),
				labels.FromStrings("group", "1", "series", "left-2"),
			},
			expectLeftSideClosedAfterOutputSeriesIndex:  1,
			expectRightSideClosedAfterOutputSeriesIndex: 0,
		},
		"unless: no more matches with unmatched series still to read on both sides": {
			isUnless: true,
			leftSeries: []labels.Labels{
				labels.FromStrings("group", "1", "series", "left-1"),
				labels.FromStrings("group", "1", "series", "left-2"),
				labels.FromStrings("group", "2", "series", "left-3"),
			},
			rightSeries: []labels.Labels{
				labels.FromStrings("group", "1", "series", "right-1"),
				labels.FromStrings("group", "1", "series", "right-2"),
				labels.FromStrings("group", "3", "series", "right-3"),
			},

			expectedOutputSeries: []labels.Labels{
				labels.FromStrings("group", "1", "series", "left-1"),
				labels.FromStrings("group", "1", "series", "left-2"),
				labels.FromStrings("group", "2", "series", "left-3"),
			},
			expectLeftSideClosedAfterOutputSeriesIndex:  2,
			expectRightSideClosedAfterOutputSeriesIndex: 0,
		},
		"and: no more matches with unmatched series still to read on left side": {
			isUnless: false,
			leftSeries: []labels.Labels{
				labels.FromStrings("group", "1", "series", "left-1"),
				labels.FromStrings("group", "1", "series", "left-2"),
				labels.FromStrings("group", "2", "series", "left-3"),
			},
			rightSeries: []labels.Labels{
				labels.FromStrings("group", "1", "series", "right-1"),
				labels.FromStrings("group", "1", "series", "right-2"),
			},

			expectedOutputSeries: []labels.Labels{
				labels.FromStrings("group", "1", "series", "left-1"),
				labels.FromStrings("group", "1", "series", "left-2"),
			},
			expectLeftSideClosedAfterOutputSeriesIndex:  1,
			expectRightSideClosedAfterOutputSeriesIndex: 0,
		},
		"unless: no more matches with unmatched series still to read on left side": {
			isUnless: true,
			leftSeries: []labels.Labels{
				labels.FromStrings("group", "1", "series", "left-1"),
				labels.FromStrings("group", "1", "series", "left-2"),
				labels.FromStrings("group", "2", "series", "left-3"),
			},
			rightSeries: []labels.Labels{
				labels.FromStrings("group", "1", "series", "right-1"),
				labels.FromStrings("group", "1", "series", "right-2"),
			},

			expectedOutputSeries: []labels.Labels{
				labels.FromStrings("group", "1", "series", "left-1"),
				labels.FromStrings("group", "1", "series", "left-2"),
				labels.FromStrings("group", "2", "series", "left-3"),
			},
			expectLeftSideClosedAfterOutputSeriesIndex:  2,
			expectRightSideClosedAfterOutputSeriesIndex: 0,
		},
		"and: no more matches with unmatched series still to read on right side": {
			isUnless: false,
			leftSeries: []labels.Labels{
				labels.FromStrings("group", "1", "series", "left-1"),
				labels.FromStrings("group", "1", "series", "left-2"),
			},
			rightSeries: []labels.Labels{
				labels.FromStrings("group", "1", "series", "right-1"),
				labels.FromStrings("group", "1", "series", "right-2"),
				labels.FromStrings("group", "3", "series", "right-3"),
			},

			expectedOutputSeries: []labels.Labels{
				labels.FromStrings("group", "1", "series", "left-1"),
				labels.FromStrings("group", "1", "series", "left-2"),
			},
			expectLeftSideClosedAfterOutputSeriesIndex:  1,
			expectRightSideClosedAfterOutputSeriesIndex: 0,
		},
		"unless: no more matches with unmatched series still to read on right side": {
			isUnless: true,
			leftSeries: []labels.Labels{
				labels.FromStrings("group", "1", "series", "left-1"),
				labels.FromStrings("group", "1", "series", "left-2"),
			},
			rightSeries: []labels.Labels{
				labels.FromStrings("group", "1", "series", "right-1"),
				labels.FromStrings("group", "1", "series", "right-2"),
				labels.FromStrings("group", "3", "series", "right-3"),
			},

			expectedOutputSeries: []labels.Labels{
				labels.FromStrings("group", "1", "series", "left-1"),
				labels.FromStrings("group", "1", "series", "left-2"),
			},
			expectLeftSideClosedAfterOutputSeriesIndex:  1,
			expectRightSideClosedAfterOutputSeriesIndex: 0,
		},
		"and: some series do not match anything on the right": {
			isUnless: false,
			leftSeries: []labels.Labels{
				labels.FromStrings("group", "1", "series", "left-1"),
				labels.FromStrings("group", "2", "series", "left-2"),
				labels.FromStrings("group", "1", "series", "left-3"),
				labels.FromStrings("group", "3", "series", "left-4"),
			},
			rightSeries: []labels.Labels{
				labels.FromStrings("group", "1", "series", "right-1"),
				labels.FromStrings("group", "1", "series", "right-2"),
				labels.FromStrings("group", "3", "series", "right-3"),
			},

			expectedOutputSeries: []labels.Labels{
				labels.FromStrings("group", "1", "series", "left-1"),
				labels.FromStrings("group", "1", "series", "left-3"),
				labels.FromStrings("group", "3", "series", "left-4"),
			},
			expectLeftSideClosedAfterOutputSeriesIndex:  2,
			expectRightSideClosedAfterOutputSeriesIndex: 2,
		},
		"and: no series match": {
			isUnless: false,
			leftSeries: []labels.Labels{
				labels.FromStrings("group", "1", "series", "left-1"),
				labels.FromStrings("group", "2", "series", "left-2"),
			},
			rightSeries: []labels.Labels{
				labels.FromStrings("group", "3", "series", "right-1"),
			},

			expectedOutputSeries:                        []labels.Labels{},
			expectLeftSideClosedAfterOutputSeriesIndex:  -1,
			expectRightSideClosedAfterOutputSeriesIndex: -1,
		},
		"unless: no series match": {
			isUnless: true,
			leftSeries: []labels.Labels{
				labels.FromStrings("group", "1", "series", "left-1"),
				labels.FromStrings("group", "2", "series", "left-2"),
			},
			rightSeries: []labels.Labels{
				labels.FromStrings("group", "3", "series", "right-1"),
			},

			expectedOutputSeries: []labels.Labels{
				labels.FromStrings("group", "1", "series", "left-1"),
				labels.FromStrings("group", "2", "series", "left-2"),
			},
			expectLeftSideClosedAfterOutputSeriesIndex:  1,
			expectRightSideClosedAfterOutputSeriesIndex: -1,
		},
		"and: no series on left": {
			isUnless:   false,
			leftSeries: []labels.Labels{},
			rightSeries: []labels.Labels{
				labels.FromStrings("group", "1", "series", "right-1"),
				labels.FromStrings("group", "2", "series", "right-2"),
				labels.FromStrings("group", "3", "series", "right-3"),
			},

			expectedOutputSeries:                        []labels.Labels{},
			expectLeftSideClosedAfterOutputSeriesIndex:  -1,
			expectRightSideClosedAfterOutputSeriesIndex: -1,
		},
		"unless: no series on left": {
			isUnless:   true,
			leftSeries: []labels.Labels{},
			rightSeries: []labels.Labels{
				labels.FromStrings("group", "1", "series", "right-1"),
				labels.FromStrings("group", "2", "series", "right-2"),
				labels.FromStrings("group", "3", "series", "right-3"),
			},

			expectedOutputSeries:                        []labels.Labels{},
			expectLeftSideClosedAfterOutputSeriesIndex:  -1,
			expectRightSideClosedAfterOutputSeriesIndex: -1,
		},
		"and: no series on right": {
			isUnless: false,
			leftSeries: []labels.Labels{
				labels.FromStrings("group", "1", "series", "left-1"),
				labels.FromStrings("group", "2", "series", "left-2"),
				labels.FromStrings("group", "3", "series", "left-3"),
			},
			rightSeries: []labels.Labels{},

			expectedOutputSeries:                        []labels.Labels{},
			expectLeftSideClosedAfterOutputSeriesIndex:  -1,
			expectRightSideClosedAfterOutputSeriesIndex: -1,
		},
		"unless: no series on right": {
			isUnless: true,
			leftSeries: []labels.Labels{
				labels.FromStrings("group", "1", "series", "left-1"),
				labels.FromStrings("group", "2", "series", "left-2"),
				labels.FromStrings("group", "3", "series", "left-3"),
			},
			rightSeries: []labels.Labels{},

			expectedOutputSeries: []labels.Labels{
				labels.FromStrings("group", "1", "series", "left-1"),
				labels.FromStrings("group", "2", "series", "left-2"),
				labels.FromStrings("group", "3", "series", "left-3"),
			},
			expectLeftSideClosedAfterOutputSeriesIndex:  2,
			expectRightSideClosedAfterOutputSeriesIndex: -1,
		},
	}

	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			if testCase.expectLeftSideClosedAfterOutputSeriesIndex >= len(testCase.expectedOutputSeries) {
				require.Failf(t, "invalid test case", "expectLeftSideClosedAfterOutputSeriesIndex %v is beyond end of expected output series %v", testCase.expectLeftSideClosedAfterOutputSeriesIndex, testCase.expectedOutputSeries)
			}

			if testCase.expectRightSideClosedAfterOutputSeriesIndex >= len(testCase.expectedOutputSeries) {
				require.Failf(t, "invalid test case", "expectRightSideClosedAfterOutputSeriesIndex %v is beyond end of expected output series %v", testCase.expectRightSideClosedAfterOutputSeriesIndex, testCase.expectedOutputSeries)
			}

			timeRange := types.NewInstantQueryTimeRange(time.Now())
			left := &operators.TestOperator{Series: testCase.leftSeries, Data: make([]types.InstantVectorSeriesData, len(testCase.leftSeries))}
			right := &operators.TestOperator{Series: testCase.rightSeries, Data: make([]types.InstantVectorSeriesData, len(testCase.rightSeries))}
			vectorMatching := parser.VectorMatching{On: true, MatchingLabels: []string{"group"}}
			memoryConsumptionTracker := limiting.NewMemoryConsumptionTracker(0, nil)
			o := NewAndUnlessBinaryOperation(left, right, vectorMatching, memoryConsumptionTracker, testCase.isUnless, timeRange, posrange.PositionRange{})

			ctx := context.Background()
			outputSeries, err := o.SeriesMetadata(ctx)
			require.NoError(t, err)

			if len(testCase.expectedOutputSeries) == 0 {
				require.Empty(t, outputSeries)
			} else {
				require.Equal(t, testutils.LabelsToSeriesMetadata(testCase.expectedOutputSeries), outputSeries)
			}

			if testCase.expectLeftSideClosedAfterOutputSeriesIndex == -1 {
				require.True(t, left.Closed, "left side should be closed after SeriesMetadata, but it is not")
			} else {
				require.False(t, left.Closed, "left side should not be closed after SeriesMetadata, but it is")
			}

			if testCase.expectRightSideClosedAfterOutputSeriesIndex == -1 {
				require.True(t, right.Closed, "right side should be closed after SeriesMetadata, but it is not")
			} else {
				require.False(t, right.Closed, "right side should not be closed after SeriesMetadata, but it is")
			}

			for outputSeriesIdx := range outputSeries {
				_, err := o.NextSeries(ctx)
				require.NoErrorf(t, err, "got error while reading series at index %v", outputSeriesIdx)

				if outputSeriesIdx >= testCase.expectLeftSideClosedAfterOutputSeriesIndex {
					require.Truef(t, left.Closed, "left side should be closed after output series at index %v, but it is not", outputSeriesIdx)
				} else {
					require.Falsef(t, left.Closed, "left side should not be closed after output series at index %v, but it is", outputSeriesIdx)
				}

				if outputSeriesIdx >= testCase.expectRightSideClosedAfterOutputSeriesIndex {
					require.Truef(t, right.Closed, "right side should be closed after output series at index %v, but it is not", outputSeriesIdx)
				} else {
					require.Falsef(t, right.Closed, "right side should not be closed after output series at index %v, but it is", outputSeriesIdx)
				}
			}

			_, err = o.NextSeries(ctx)
			require.Equal(t, types.EOS, err)

			o.Close()
			// Make sure we've returned everything to their pools.
			require.Equal(t, uint64(0), memoryConsumptionTracker.CurrentEstimatedMemoryConsumptionBytes)
		})
	}
}
