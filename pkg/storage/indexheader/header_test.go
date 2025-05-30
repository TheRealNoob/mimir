// SPDX-License-Identifier: AGPL-3.0-only
// Provenance-includes-location: https://github.com/thanos-io/thanos/blob/main/pkg/block/indexheader/header_test.go
// Provenance-includes-license: Apache-2.0
// Provenance-includes-copyright: The Thanos Authors.

package indexheader

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/go-kit/log"
	"github.com/grafana/dskit/gate"
	"github.com/oklog/ulid/v2"
	"github.com/pkg/errors"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/tsdb/encoding"
	"github.com/prometheus/prometheus/tsdb/fileutil"
	"github.com/prometheus/prometheus/tsdb/index"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thanos-io/objstore"
	"github.com/thanos-io/objstore/providers/filesystem"

	"github.com/grafana/mimir/pkg/storage/tsdb/block"
	"github.com/grafana/mimir/pkg/util/test"
)

var implementations = []struct {
	name    string
	factory func(t *testing.T, ctx context.Context, dir string, id ulid.ULID) Reader
}{
	{
		name: "stream binary reader",
		factory: func(t *testing.T, ctx context.Context, dir string, id ulid.ULID) Reader {
			br, err := NewStreamBinaryReader(ctx, log.NewNopLogger(), nil, dir, id, 32, NewStreamBinaryReaderMetrics(nil), Config{})
			require.NoError(t, err)
			requireCleanup(t, br.Close)
			return br
		},
	},
	{
		name: "lazy stream binary reader",
		factory: func(t *testing.T, ctx context.Context, dir string, id ulid.ULID) Reader {
			readerFactory := func() (Reader, error) {
				return NewStreamBinaryReader(ctx, log.NewNopLogger(), nil, dir, id, 32, NewStreamBinaryReaderMetrics(nil), Config{})
			}

			br, err := NewLazyBinaryReader(ctx, readerFactory, log.NewNopLogger(), nil, dir, id, NewLazyBinaryReaderMetrics(nil), nil, gate.NewNoop())
			require.NoError(t, err)
			requireCleanup(t, br.Close)
			return br
		},
	},
}

func TestReadersComparedToIndexHeader(t *testing.T) {
	ctx := context.Background()

	tmpDir := t.TempDir()
	bkt, err := filesystem.NewBucket(filepath.Join(tmpDir, "bkt"))
	require.NoError(t, err)
	requireCleanup(t, bkt.Close)

	// Create block index version 2.
	series := []labels.Labels{
		labels.FromStrings("a", "1"),
		labels.FromStrings("a", "2"),
		labels.FromStrings("a", "3"),
		labels.FromStrings("a", "4"),
		labels.FromStrings("a", "5"),
		labels.FromStrings("a", "6"),
		labels.FromStrings("a", "7"),
		labels.FromStrings("a", "8"),
		labels.FromStrings("a", "9"),
		// Missing 10 on purpose.
		labels.FromStrings("a", "11"),
		labels.FromStrings("a", "12"),
		labels.FromStrings("a", "13"),
		labels.FromStrings("a", "1", "longer-string", "1"),
		labels.FromStrings("a", "1", "longer-string", "2"),
	}

	idIndexV2, err := block.CreateBlock(ctx, tmpDir, series, 100, 0, 1000, labels.FromStrings("ext1", "1"))
	require.NoError(t, err)
	require.NoError(t, block.Upload(ctx, log.NewNopLogger(), bkt, filepath.Join(tmpDir, idIndexV2.String()), nil))

	metaIndexV1, err := block.ReadMetaFromDir("./testdata/index_format_v1")
	require.NoError(t, err)
	test.Copy(t, "./testdata/index_format_v1", filepath.Join(tmpDir, metaIndexV1.ULID.String()))

	_, err = block.InjectThanosMeta(log.NewNopLogger(), filepath.Join(tmpDir, metaIndexV1.ULID.String()), block.ThanosMeta{
		Labels: labels.FromStrings("ext1", "1").Map(),
		Source: block.TestSource,
	}, &metaIndexV1.BlockMeta)

	require.NoError(t, err)
	require.NoError(t, block.Upload(ctx, log.NewNopLogger(), bkt, filepath.Join(tmpDir, metaIndexV1.ULID.String()), nil))

	for _, testBlock := range []struct {
		version string
		id      ulid.ULID
	}{
		{version: "v2", id: idIndexV2},
		{version: "v1", id: metaIndexV1.ULID},
	} {
		t.Run(testBlock.version, func(t *testing.T) {
			id := testBlock.id
			indexName := filepath.Join(tmpDir, id.String(), block.IndexHeaderFilename)
			require.NoError(t, WriteBinary(ctx, bkt, id, indexName))

			indexFile, err := fileutil.OpenMmapFile(filepath.Join(tmpDir, id.String(), block.IndexFilename))
			require.NoError(t, err)
			requireCleanup(t, indexFile.Close)

			b := realByteSlice(indexFile.Bytes())
			for _, impl := range implementations {
				t.Run(impl.name, func(t *testing.T) {
					r := impl.factory(t, ctx, tmpDir, id)
					compareIndexToHeader(t, b, r)
				})
			}

		})
	}

}

func Test_DownsampleSparseIndexHeader(t *testing.T) {
	tests := map[string]struct {
		protoRate         int
		inMemSamplingRate int
		expected          map[string]int
	}{
		"downsample_1_to_32": {
			protoRate:         1,
			inMemSamplingRate: 32,
			expected: map[string]int{
				"__name__":            4,
				"":                    1,
				"__blockgen_target__": 4,
			},
		},
		"downsample_4_to_16": {
			protoRate:         4,
			inMemSamplingRate: 16,
			expected: map[string]int{
				"__name__":            7,
				"":                    1,
				"__blockgen_target__": 7,
			},
		},
		"downsample_8_to_24": {
			protoRate:         8,
			inMemSamplingRate: 24,
			expected: map[string]int{
				"__name__":            5,
				"":                    1,
				"__blockgen_target__": 5,
			},
		},
		"downsample_17_to_51": {
			protoRate:         17,
			inMemSamplingRate: 51,
			expected: map[string]int{
				"__name__":            3,
				"":                    1,
				"__blockgen_target__": 3,
			},
		},
		"noop_on_same_sampling_rate": {
			protoRate:         32,
			inMemSamplingRate: 32,
		},
		"rebuild_proto_sampling_rate_not_divisible": {
			protoRate:         8,
			inMemSamplingRate: 20,
		},
		"rebuild_cannot_upsample_from_proto_48_to_32": {
			protoRate:         48,
			inMemSamplingRate: 32,
		},
		"rebuild_cannot_upsample_from_proto_64_to_32": {
			protoRate:         64,
			inMemSamplingRate: 32,
		},
		"downsample_to_low_frequency": {
			protoRate:         4,
			inMemSamplingRate: 16384,
			expected: map[string]int{
				"__name__":            2,
				"":                    1,
				"__blockgen_target__": 2,
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			m, err := block.ReadMetaFromDir("./testdata/index_format_v2")
			require.NoError(t, err)

			tmpDir := t.TempDir()
			test.Copy(t, "./testdata/index_format_v2", filepath.Join(tmpDir, m.ULID.String()))

			ubkt, err := filesystem.NewBucket(tmpDir)
			require.NoError(t, err)

			bkt := objstore.WithNoopInstr(ubkt)

			t.Cleanup(func() {
				require.NoError(t, ubkt.Close())
				require.NoError(t, bkt.Close())
			})

			ctx := context.Background()
			noopMetrics := NewStreamBinaryReaderMetrics(nil)

			// write a sparse index-header file to disk
			br1, err := NewStreamBinaryReader(ctx, log.NewNopLogger(), bkt, tmpDir, m.ULID, tt.protoRate, noopMetrics, Config{})
			require.NoError(t, err)
			require.Equal(t, tt.protoRate, br1.postingsOffsetTable.PostingOffsetInMemSampling())

			origLabelNames, err := br1.postingsOffsetTable.LabelNames()
			require.NoError(t, err)

			// a second call to NewStreamBinaryReader loads the previously written sparse index-header and downsamples
			// the header from tt.protoRate to tt.inMemSamplingRate entries for each posting
			br2, err := NewStreamBinaryReader(ctx, log.NewNopLogger(), bkt, tmpDir, m.ULID, tt.inMemSamplingRate, noopMetrics, Config{})
			require.NoError(t, err)
			require.Equal(t, tt.inMemSamplingRate, br2.postingsOffsetTable.PostingOffsetInMemSampling())

			downsampleLabelNames, err := br2.postingsOffsetTable.LabelNames()
			require.NoError(t, err)

			// label names are equal between original and downsampled sparse index-headers
			require.ElementsMatch(t, downsampleLabelNames, origLabelNames)

			origIdxpbTbl := br1.postingsOffsetTable.NewSparsePostingOffsetTable()
			downsampleIdxpbTbl := br2.postingsOffsetTable.NewSparsePostingOffsetTable()

			for name, vals := range origIdxpbTbl.Postings {
				downsampledOffsets := downsampleIdxpbTbl.Postings[name].Offsets
				// downsampled postings are a subset of the original sparse index-header postings
				if (tt.inMemSamplingRate > tt.protoRate) && (tt.inMemSamplingRate%tt.protoRate == 0) {
					require.Equal(t, tt.expected[name], len(downsampledOffsets))
					require.Subset(t, vals.Offsets, downsampledOffsets, "downsampled offsets not a subset of original for name '%s'", name)

					require.Equal(t, downsampledOffsets[0], vals.Offsets[0], "downsampled offsets do not contain first value for name '%s'", name)
					require.Equal(t, downsampledOffsets[len(downsampledOffsets)-1], vals.Offsets[len(vals.Offsets)-1], "downsampled offsets do not contain last value for name '%s'", name)
				}

				// check first and last entry from the original postings in downsampled set
				require.NotZero(t, downsampleIdxpbTbl.Postings[name].LastValOffset)
			}
		})
	}
}

func compareIndexToHeaderPostings(t *testing.T, indexByteSlice index.ByteSlice, sbr *StreamBinaryReader) {

	ir, err := index.NewReader(indexByteSlice, index.DecodePostingsRaw)
	require.NoError(t, err)
	defer func() {
		_ = ir.Close()
	}()

	toc, err := index.NewTOCFromByteSlice(indexByteSlice)
	require.NoError(t, err)

	tblOffsetBounds := make(map[string][2]int64)

	// Read the postings offset table and record first and last offset for each label. Adjust offsets in ReadPostingsOffsetTable
	// by 4B (int32 count of postings in table) to align with postings in index headers.
	err = index.ReadPostingsOffsetTable(indexByteSlice, toc.PostingsTable, func(label []byte, _ []byte, _ uint64, offset int) error {
		name := string(label)
		off := int64(offset + 4)
		if v, ok := tblOffsetBounds[name]; ok {
			v[1] = off
			tblOffsetBounds[name] = v
		} else {
			tblOffsetBounds[name] = [2]int64{off, off}
		}
		return nil
	})
	require.NoError(t, err)

	tbl := sbr.postingsOffsetTable.NewSparsePostingOffsetTable()

	expLabelNames, err := ir.LabelNames(context.Background())
	require.NoError(t, err)
	for _, lname := range expLabelNames {
		offsets := tbl.Postings[lname].Offsets
		assert.Equal(t, offsets[0].TableOff, tblOffsetBounds[lname][0])
		assert.Equal(t, offsets[len(offsets)-1].TableOff, tblOffsetBounds[lname][1])
	}
}

func compareIndexToHeader(t *testing.T, indexByteSlice index.ByteSlice, headerReader Reader) {
	ctx := context.Background()

	indexReader, err := index.NewReader(indexByteSlice, index.DecodePostingsRaw)
	require.NoError(t, err)
	defer func() { _ = indexReader.Close() }()

	actVersion, err := headerReader.IndexVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, indexReader.Version(), actVersion)

	if indexReader.Version() == index.FormatV2 {
		// For v2 symbols ref sequential integers 0, 1, 2 etc.
		iter := indexReader.Symbols()
		i := 0
		for iter.Next() {
			r, err := headerReader.LookupSymbol(ctx, uint32(i))
			require.NoError(t, err)
			require.Equal(t, iter.At(), r)

			i++
		}
		require.NoError(t, iter.Err())
		_, err := headerReader.LookupSymbol(ctx, uint32(i))
		require.Error(t, err)
	} else {
		// For v1 symbols refs are actual offsets in the index.
		symbols, err := getSymbolTable(indexByteSlice)
		require.NoError(t, err)

		for refs, sym := range symbols {
			r, err := headerReader.LookupSymbol(ctx, refs)
			require.NoError(t, err)
			require.Equal(t, sym, r)
		}
		_, err = headerReader.LookupSymbol(ctx, 200000)
		require.Error(t, err)
	}

	expLabelNames, err := indexReader.LabelNames(ctx)
	require.NoError(t, err)
	actualLabelNames, err := headerReader.LabelNames(ctx)
	require.NoError(t, err)
	require.Equal(t, expLabelNames, actualLabelNames)

	expRanges, err := indexReader.PostingsRanges()
	require.NoError(t, err)

	for _, lname := range expLabelNames {
		expectedLabelVals, err := indexReader.SortedLabelValues(ctx, lname)
		require.NoError(t, err)

		valOffsets, err := headerReader.LabelValuesOffsets(ctx, lname, "", nil)
		require.NoError(t, err)
		strValsFromOffsets := make([]string, len(valOffsets))
		for i := range valOffsets {
			strValsFromOffsets[i] = valOffsets[i].LabelValue
		}
		require.Equal(t, expectedLabelVals, strValsFromOffsets)

		for _, v := range valOffsets {
			ptr, err := headerReader.PostingsOffset(ctx, lname, v.LabelValue)
			require.NoError(t, err)
			label := labels.Label{Name: lname, Value: v.LabelValue}
			assert.Equal(t, expRanges[label], ptr)
			assert.Equal(t, expRanges[label], v.Off)
			delete(expRanges, label)
		}
	}

	allPName, allPValue := index.AllPostingsKey()
	ptr, err := headerReader.PostingsOffset(ctx, allPName, allPValue)
	require.NoError(t, err)

	emptyLabel := labels.Label{Name: "", Value: ""}
	require.Equal(t, expRanges[emptyLabel].Start, ptr.Start)
	require.Equal(t, expRanges[emptyLabel].End, ptr.End)
	delete(expRanges, emptyLabel)
	require.Empty(t, expRanges)
}

func prepareIndexV2Block(t testing.TB, tmpDir string, bkt objstore.Bucket) *block.Meta {
	/* Copy index 6MB block index version 2. It was generated via thanosbench. Meta.json:
		{
		"ulid": "01DRBP4RNVZ94135ZA6B10EMRR",
		"minTime": 1570766415000,
		"maxTime": 1570939215001,
		"stats": {
			"numSamples": 115210000,
			"numSeries": 10000,
			"numChunks": 990000
		},
		"compaction": {
			"level": 1,
			"sources": [
				"01DRBP4RNVZ94135ZA6B10EMRR"
			]
		},
		"version": 1,
		"thanos": {
			"labels": {
				"cluster": "one",
				"dataset": "continuous"
			},
			"downsample": {
				"resolution": 0
			},
			"source": "blockgen"
		}
	}
	*/

	m, err := block.ReadMetaFromDir("./testdata/index_format_v2")
	require.NoError(t, err)
	test.Copy(t, "./testdata/index_format_v2", filepath.Join(tmpDir, m.ULID.String()))

	_, err = block.InjectThanosMeta(log.NewNopLogger(), filepath.Join(tmpDir, m.ULID.String()), block.ThanosMeta{
		Labels: labels.FromStrings("ext1", "1").Map(),
		Source: block.TestSource,
	}, &m.BlockMeta)
	require.NoError(t, err)
	require.NoError(t, block.Upload(context.Background(), log.NewNopLogger(), bkt, filepath.Join(tmpDir, m.ULID.String()), nil))

	return m
}

func TestReadersLabelValuesOffsets(t *testing.T) {
	tests, blockID, blockDir := labelValuesTestCases(test.NewTB(t))
	for _, impl := range implementations {
		t.Run(impl.name, func(t *testing.T) {
			r := impl.factory(t, context.Background(), blockDir, blockID)
			for lbl, tcs := range tests {
				t.Run(lbl, func(t *testing.T) {
					for _, tc := range tcs {
						t.Run(fmt.Sprintf("prefix='%s'%s", tc.prefix, tc.desc), func(t *testing.T) {
							values, err := r.LabelValuesOffsets(context.Background(), lbl, tc.prefix, tc.filter)
							require.NoError(t, err)
							require.Equal(t, tc.expected, len(values))
						})
					}
				})
			}
		})
	}
}

func TestConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		setup       func(*Config)
		expectedErr error
	}{
		"should fail on invalid index-header lazy loading max concurrency": {
			setup: func(cfg *Config) {
				cfg.LazyLoadingConcurrency = -1
			},
			expectedErr: errInvalidIndexHeaderLazyLoadingConcurrency,
		},
	}

	for testName, testData := range tests {
		testData := testData

		t.Run(testName, func(t *testing.T) {
			indexHeaderConfig := &Config{}

			fs := flag.NewFlagSet("", flag.PanicOnError)
			indexHeaderConfig.RegisterFlagsWithPrefix(fs, "blocks-storage.bucket-store.index-header.")

			testData.setup(indexHeaderConfig)

			actualErr := indexHeaderConfig.Validate()
			assert.Equal(t, testData.expectedErr, actualErr)
		})
	}
}

type labelValuesTestCase struct {
	prefix   string
	desc     string
	filter   func(string) bool
	expected int
}

func labelValuesTestCases(t test.TB) (tests map[string][]labelValuesTestCase, blockID ulid.ULID, bucketDir string) {
	const testLabelCount = 32
	const testSeriesCount = 512

	ctx := context.Background()

	tmpDir := t.TempDir()
	bkt, err := filesystem.NewBucket(filepath.Join(tmpDir, "bkt"))
	require.NoError(t, err)
	requireCleanup(t, bkt.Close)

	series := make([]labels.Labels, 0, testSeriesCount)
	lblValues := make([]int, testLabelCount)
	for i := 0; i < testSeriesCount; i++ {
		// add first, so we'll have the value_000.
		lblStrings := make([]string, 0, testLabelCount*2)
		for idx, val := range lblValues {
			lblStrings = append(lblStrings, fmt.Sprintf("test_label_%d", idx), fmt.Sprintf("value_%03d", val))
		}
		series = append(series, labels.FromStrings(lblStrings...))

		for idx := range lblValues {
			lblValues[idx]++
			if idx < 2 {
				continue
			}
			lblValues[idx] %= idx
		}
	}

	id, err := block.CreateBlock(ctx, tmpDir, series, 100, 0, 1000, labels.FromStrings("ext1", "1"))
	require.NoError(t, err)
	require.NoError(t, block.Upload(ctx, log.NewNopLogger(), bkt, filepath.Join(tmpDir, id.String()), nil))

	indexName := filepath.Join(tmpDir, id.String(), block.IndexHeaderFilename)
	require.NoError(t, WriteBinary(ctx, bkt, id, indexName))

	indexFile, err := fileutil.OpenMmapFile(filepath.Join(tmpDir, id.String(), block.IndexFilename))
	require.NoError(t, err)
	requireCleanup(t, indexFile.Close)

	tests = map[string][]labelValuesTestCase{
		"test_label_0": {
			{prefix: "", expected: 512},
			{prefix: "value_", expected: 512},
			{prefix: "value_0", expected: 100},
			{prefix: "value_1", expected: 100},
			{prefix: "value_2", expected: 100},
			{prefix: "value_3", expected: 100},
			{prefix: "value_4", expected: 100},
			{prefix: "value_5", expected: 12},
			{prefix: "value_00", expected: 10},
			{prefix: "value_10", expected: 10},
			{prefix: "value_20", expected: 10},
			{prefix: "value_30", expected: 10},
			{prefix: "value_40", expected: 10},
			{prefix: "value_50", expected: 10},
			{prefix: "value_000", expected: 1},
			{prefix: "value_400", expected: 1},
			{prefix: "value_511", expected: 1},
			{prefix: "value_512", expected: 0},
			{prefix: "value_600", expected: 0},
			{prefix: "value_aaa", expected: 0},
			{prefix: "value_0000", expected: 0},
			{prefix: "value_5110", expected: 0},
			{
				prefix:   "value_",
				desc:     " only even",
				filter:   labels.MustNewMatcher(labels.MatchRegexp, "test_label_0", "value_[0-9][0-9][02468]").Matches,
				expected: 256,
			},
			{
				prefix:   "",
				desc:     " only even",
				filter:   labels.MustNewMatcher(labels.MatchRegexp, "test_label_0", "value_[0-9][0-9][02468]").Matches,
				expected: 256,
			},
		},
	}

	for lblIdx := 2; lblIdx < testLabelCount; lblIdx++ {
		lbl := fmt.Sprintf("test_label_%d", lblIdx)
		tests[lbl] = append(tests[lbl],
			labelValuesTestCase{prefix: "", expected: lblIdx},
			labelValuesTestCase{prefix: "value_", expected: lblIdx},
			labelValuesTestCase{prefix: "value_000", expected: 1},
			labelValuesTestCase{prefix: "value_001", expected: 1},
			labelValuesTestCase{prefix: fmt.Sprintf("value_%03d", lblIdx-1), expected: 1},
			labelValuesTestCase{prefix: fmt.Sprintf("value_%03d", lblIdx), expected: 0},
		)
	}

	return tests, id, tmpDir
}

func BenchmarkBinaryWrite(t *testing.B) {
	ctx := context.Background()

	tmpDir := t.TempDir()
	bkt, err := filesystem.NewBucket(filepath.Join(tmpDir, "bkt"))
	require.NoError(t, err)
	defer func() { require.NoError(t, bkt.Close()) }()

	m := prepareIndexV2Block(t, tmpDir, bkt)
	fn := filepath.Join(tmpDir, m.ULID.String(), block.IndexHeaderFilename)

	t.ResetTimer()
	for i := 0; i < t.N; i++ {
		require.NoError(t, WriteBinary(ctx, bkt, m.ULID, fn))
	}
}

func getSymbolTable(b index.ByteSlice) (map[uint32]string, error) {
	version := int(b.Range(4, 5)[0])

	if version != 1 && version != 2 {
		return nil, errors.Errorf("unknown index file version %d", version)
	}

	toc, err := index.NewTOCFromByteSlice(b)
	if err != nil {
		return nil, errors.Wrap(err, "read TOC")
	}

	symbolsV2, symbolsV1, err := readSymbols(b, version, int(toc.Symbols))
	if err != nil {
		return nil, errors.Wrap(err, "read symbols")
	}

	symbolsTable := make(map[uint32]string, len(symbolsV1)+len(symbolsV2))
	for o, s := range symbolsV1 {
		symbolsTable[o] = s
	}
	for o, s := range symbolsV2 {
		symbolsTable[uint32(o)] = s
	}
	return symbolsTable, nil
}

// readSymbols reads the symbol table fully into memory and allocates proper strings for them.
// Strings backed by the mmap'd memory would cause memory faults if applications keep using them
// after the reader is closed.
func readSymbols(bs index.ByteSlice, version, off int) ([]string, map[uint32]string, error) {
	if off == 0 {
		return nil, nil, nil
	}
	d := encoding.NewDecbufAt(bs, off, castagnoliTable)

	var (
		origLen     = d.Len()
		cnt         = d.Be32int()
		basePos     = uint32(off) + 4
		nextPos     = basePos + uint32(origLen-d.Len())
		symbolSlice []string
		symbols     = map[uint32]string{}
	)
	if version == index.FormatV2 {
		symbolSlice = make([]string, 0, cnt)
	}

	for d.Err() == nil && d.Len() > 0 && cnt > 0 {
		s := d.UvarintStr()

		if version == index.FormatV2 {
			symbolSlice = append(symbolSlice, s)
		} else {
			symbols[nextPos] = s
			nextPos = basePos + uint32(origLen-d.Len())
		}
		cnt--
	}
	return symbolSlice, symbols, errors.Wrap(d.Err(), "read symbols")
}

func requireCleanup(t testing.TB, cleanupFun func() error) {
	t.Cleanup(func() {
		require.NoError(t, cleanupFun())
	})
}
