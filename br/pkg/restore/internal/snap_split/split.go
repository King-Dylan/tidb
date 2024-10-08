// Copyright 2020 PingCAP, Inc. Licensed under Apache-2.0.

package snapsplit

import (
	"context"
	"time"

	"github.com/pingcap/errors"
	"github.com/pingcap/log"
	"github.com/pingcap/tidb/br/pkg/restore/split"
	"go.uber.org/zap"
)

// RegionSplitter is a executor of region split by rules.
type RegionSplitter struct {
	client split.SplitClient
}

// NewRegionSplitter returns a new RegionSplitter.
func NewRegionSplitter(client split.SplitClient) *RegionSplitter {
	return &RegionSplitter{
		client: client,
	}
}

// SplitWaitAndScatter expose the function `SplitWaitAndScatter` of split client.
func (rs *RegionSplitter) SplitWaitAndScatter(ctx context.Context, region *split.RegionInfo, keys [][]byte) ([]*split.RegionInfo, error) {
	return rs.client.SplitWaitAndScatter(ctx, region, keys)
}

// ExecuteSplit executes regions split and make sure new splitted regions are balance.
// It will split regions by the rewrite rules,
// then it will split regions by the end key of each range.
// tableRules includes the prefix of a table, since some ranges may have
// a prefix with record sequence or index sequence.
// note: all ranges and rewrite rules must have raw key.
func (rs *RegionSplitter) ExecuteSplit(
	ctx context.Context,
	sortedSplitKeys [][]byte,
) error {
	if len(sortedSplitKeys) == 0 {
		log.Info("skip split regions, no split keys")
		return nil
	}

	log.Info("execute split sorted keys", zap.Int("keys count", len(sortedSplitKeys)))
	return rs.executeSplitByRanges(ctx, sortedSplitKeys)
}

func (rs *RegionSplitter) executeSplitByRanges(
	ctx context.Context,
	sortedKeys [][]byte,
) error {
	startTime := time.Now()
	// Choose the rough region split keys,
	// each splited region contains 128 regions to be splitted.
	const regionIndexStep = 128

	roughSortedSplitKeys := make([][]byte, 0, len(sortedKeys)/regionIndexStep+1)
	for curRegionIndex := regionIndexStep; curRegionIndex < len(sortedKeys); curRegionIndex += regionIndexStep {
		roughSortedSplitKeys = append(roughSortedSplitKeys, sortedKeys[curRegionIndex])
	}
	if len(roughSortedSplitKeys) > 0 {
		if err := rs.executeSplitByKeys(ctx, roughSortedSplitKeys); err != nil {
			return errors.Trace(err)
		}
	}
	log.Info("finish spliting regions roughly", zap.Duration("take", time.Since(startTime)))

	// Then send split requests to each TiKV.
	if err := rs.executeSplitByKeys(ctx, sortedKeys); err != nil {
		return errors.Trace(err)
	}

	log.Info("finish spliting and scattering regions", zap.Duration("take", time.Since(startTime)))
	return nil
}

// executeSplitByKeys will split regions by **sorted** keys with following steps.
// 1. locate regions with correspond keys.
// 2. split these regions with correspond keys.
// 3. make sure new split regions are balanced.
func (rs *RegionSplitter) executeSplitByKeys(
	ctx context.Context,
	sortedKeys [][]byte,
) error {
	startTime := time.Now()
	scatterRegions, err := rs.client.SplitKeysAndScatter(ctx, sortedKeys)
	if err != nil {
		return errors.Trace(err)
	}
	if len(scatterRegions) > 0 {
		log.Info("finish splitting and scattering regions. and starts to wait", zap.Int("regions", len(scatterRegions)),
			zap.Duration("take", time.Since(startTime)))
		rs.waitRegionsScattered(ctx, scatterRegions, split.ScatterWaitUpperInterval)
	} else {
		log.Info("finish splitting regions.", zap.Duration("take", time.Since(startTime)))
	}
	return nil
}

// waitRegionsScattered try to wait mutilple regions scatterd in 3 minutes.
// this could timeout, but if many regions scatterd the restore could continue
// so we don't wait long time here.
func (rs *RegionSplitter) waitRegionsScattered(ctx context.Context, scatterRegions []*split.RegionInfo, timeout time.Duration) {
	log.Info("start to wait for scattering regions", zap.Int("regions", len(scatterRegions)))
	startTime := time.Now()
	leftCnt := rs.WaitForScatterRegionsTimeout(ctx, scatterRegions, timeout)
	if leftCnt == 0 {
		log.Info("waiting for scattering regions done",
			zap.Int("regions", len(scatterRegions)),
			zap.Duration("take", time.Since(startTime)))
	} else {
		log.Warn("waiting for scattering regions timeout",
			zap.Int("not scattered Count", leftCnt),
			zap.Int("regions", len(scatterRegions)),
			zap.Duration("take", time.Since(startTime)))
	}
}

func (rs *RegionSplitter) WaitForScatterRegionsTimeout(ctx context.Context, regionInfos []*split.RegionInfo, timeout time.Duration) int {
	ctx2, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	leftRegions, _ := rs.client.WaitRegionsScattered(ctx2, regionInfos)
	return leftRegions
}
