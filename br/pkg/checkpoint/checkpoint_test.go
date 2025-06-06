// Copyright 2022 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package checkpoint_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/pingcap/failpoint"
	backuppb "github.com/pingcap/kvproto/pkg/brpb"
	"github.com/pingcap/kvproto/pkg/encryptionpb"
	"github.com/pingcap/tidb/br/pkg/checkpoint"
	"github.com/pingcap/tidb/br/pkg/gluetidb"
	"github.com/pingcap/tidb/br/pkg/pdutil"
	"github.com/pingcap/tidb/br/pkg/storage"
	"github.com/pingcap/tidb/br/pkg/utiltest"
	"github.com/pingcap/tidb/pkg/meta/model"
	"github.com/pingcap/tidb/pkg/parser/ast"
	"github.com/stretchr/testify/require"
	"github.com/tikv/client-go/v2/oracle"
)

func TestCheckpointMetaForBackup(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	s, err := storage.NewLocalStorage(base)
	require.NoError(t, err)

	checkpointMeta := &checkpoint.CheckpointMetadataForBackup{
		ConfigHash: []byte("123456"),
		BackupTS:   123456,
	}

	err = checkpoint.SaveCheckpointMetadata(ctx, s, checkpointMeta)
	require.NoError(t, err)

	checkpointMeta2, err := checkpoint.LoadCheckpointMetadata(ctx, s)
	require.NoError(t, err)
	require.Equal(t, checkpointMeta.ConfigHash, checkpointMeta2.ConfigHash)
	require.Equal(t, checkpointMeta.BackupTS, checkpointMeta2.BackupTS)
}

func TestCheckpointMetaForRestoreOnStorage(t *testing.T) {
	base := t.TempDir()
	s, err := storage.NewLocalStorage(base)
	require.NoError(t, err)
	snapshotMetaManager := checkpoint.NewSnapshotStorageMetaManager(s, nil, 1, "snapshot", 1)
	defer snapshotMetaManager.Close()
	logMetaManager := checkpoint.NewLogStorageMetaManager(s, nil, 1, "log", 1)
	defer logMetaManager.Close()
	testCheckpointMetaForRestore(t, snapshotMetaManager, logMetaManager)
}

func TestCheckpointMetaForRestoreOnTable(t *testing.T) {
	s := utiltest.CreateRestoreSchemaSuite(t)
	g := gluetidb.New()
	snapshotMetaManager, err := checkpoint.NewSnapshotTableMetaManager(g, s.Mock.Domain, checkpoint.SnapshotRestoreCheckpointDatabaseName, 1)
	require.NoError(t, err)
	defer snapshotMetaManager.Close()
	logMetaManager, err := checkpoint.NewLogTableMetaManager(g, s.Mock.Domain, checkpoint.LogRestoreCheckpointDatabaseName, 1)
	require.NoError(t, err)
	defer logMetaManager.Close()
	testCheckpointMetaForRestore(t, snapshotMetaManager, logMetaManager)
}

func testCheckpointMetaForRestore(
	t *testing.T,
	snapshotMetaManager checkpoint.SnapshotMetaManagerT,
	logMetaManager checkpoint.LogMetaManagerT,
) {
	ctx := context.Background()

	checkpointMetaForSnapshotRestore := &checkpoint.CheckpointMetadataForSnapshotRestore{
		UpstreamClusterID: 123,
		RestoredTS:        321,
		SchedulersConfig: &pdutil.ClusterConfig{
			Schedulers: []string{"1", "2"},
			ScheduleCfg: map[string]any{
				"1": "2",
				"2": "1",
			},
		},
	}
	err := snapshotMetaManager.SaveCheckpointMetadata(ctx, checkpointMetaForSnapshotRestore)
	require.NoError(t, err)
	checkpointMetaForSnapshotRestore2, err := snapshotMetaManager.LoadCheckpointMetadata(ctx)
	require.NoError(t, err)
	require.Equal(t, checkpointMetaForSnapshotRestore.SchedulersConfig, checkpointMetaForSnapshotRestore2.SchedulersConfig)
	require.Equal(t, checkpointMetaForSnapshotRestore.UpstreamClusterID, checkpointMetaForSnapshotRestore2.UpstreamClusterID)
	require.Equal(t, checkpointMetaForSnapshotRestore.RestoredTS, checkpointMetaForSnapshotRestore2.RestoredTS)

	checkpointMetaForLogRestore := &checkpoint.CheckpointMetadataForLogRestore{
		UpstreamClusterID: 123,
		RestoredTS:        222,
		StartTS:           111,
		RewriteTS:         333,
		GcRatio:           "1.0",
		TiFlashItems:      map[int64]model.TiFlashReplicaInfo{1: {Count: 1}},
	}

	err = logMetaManager.SaveCheckpointMetadata(ctx, checkpointMetaForLogRestore)
	require.NoError(t, err)
	checkpointMetaForLogRestore2, err := logMetaManager.LoadCheckpointMetadata(ctx)
	require.NoError(t, err)
	require.Equal(t, checkpointMetaForLogRestore.UpstreamClusterID, checkpointMetaForLogRestore2.UpstreamClusterID)
	require.Equal(t, checkpointMetaForLogRestore.RestoredTS, checkpointMetaForLogRestore2.RestoredTS)
	require.Equal(t, checkpointMetaForLogRestore.StartTS, checkpointMetaForLogRestore2.StartTS)
	require.Equal(t, checkpointMetaForLogRestore.RewriteTS, checkpointMetaForLogRestore2.RewriteTS)
	require.Equal(t, checkpointMetaForLogRestore.GcRatio, checkpointMetaForLogRestore2.GcRatio)
	require.Equal(t, checkpointMetaForLogRestore.TiFlashItems, checkpointMetaForLogRestore2.TiFlashItems)

	exists, err := logMetaManager.ExistsCheckpointProgress(ctx)
	require.NoError(t, err)
	require.False(t, exists)
	err = logMetaManager.SaveCheckpointProgress(ctx, &checkpoint.CheckpointProgress{
		Progress: checkpoint.InLogRestoreAndIdMapPersisted,
	})
	require.NoError(t, err)
	progress, err := logMetaManager.LoadCheckpointProgress(ctx)
	require.NoError(t, err)
	require.Equal(t, checkpoint.InLogRestoreAndIdMapPersisted, progress.Progress)

	taskInfo, err := checkpoint.GetCheckpointTaskInfo(ctx, snapshotMetaManager, logMetaManager)
	require.NoError(t, err)
	require.Equal(t, uint64(123), taskInfo.Metadata.UpstreamClusterID)
	require.Equal(t, uint64(222), taskInfo.Metadata.RestoredTS)
	require.Equal(t, uint64(111), taskInfo.Metadata.StartTS)
	require.Equal(t, uint64(333), taskInfo.Metadata.RewriteTS)
	require.Equal(t, "1.0", taskInfo.Metadata.GcRatio)
	require.Equal(t, true, taskInfo.HasSnapshotMetadata)
	require.Equal(t, checkpoint.InLogRestoreAndIdMapPersisted, taskInfo.Progress)

	exists, err = logMetaManager.ExistsCheckpointIngestIndexRepairSQLs(ctx)
	require.NoError(t, err)
	require.False(t, exists)
	err = logMetaManager.SaveCheckpointIngestIndexRepairSQLs(ctx, &checkpoint.CheckpointIngestIndexRepairSQLs{
		SQLs: []checkpoint.CheckpointIngestIndexRepairSQL{
			{
				IndexID:    1,
				SchemaName: ast.NewCIStr("2"),
				TableName:  ast.NewCIStr("3"),
				IndexName:  "4",
				AddSQL:     "5",
				AddArgs:    []any{"6", "7", "8"},
			},
		},
	})
	require.NoError(t, err)
	repairSQLs, err := logMetaManager.LoadCheckpointIngestIndexRepairSQLs(ctx)
	require.NoError(t, err)
	require.Equal(t, repairSQLs.SQLs[0].IndexID, int64(1))
	require.Equal(t, repairSQLs.SQLs[0].SchemaName, ast.NewCIStr("2"))
	require.Equal(t, repairSQLs.SQLs[0].TableName, ast.NewCIStr("3"))
	require.Equal(t, repairSQLs.SQLs[0].IndexName, "4")
	require.Equal(t, repairSQLs.SQLs[0].AddSQL, "5")
	require.Equal(t, repairSQLs.SQLs[0].AddArgs, []any{"6", "7", "8"})
}

type mockTimer struct {
	p int64
	l int64
}

func NewMockTimer(p, l int64) *mockTimer {
	return &mockTimer{p: p, l: l}
}

func (t *mockTimer) GetTS(ctx context.Context) (int64, int64, error) {
	return t.p, t.l, nil
}

func TestCheckpointBackupRunner(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	s, err := storage.NewLocalStorage(base)
	require.NoError(t, err)
	os.MkdirAll(base+checkpoint.CheckpointDataDirForBackup, 0o755)
	os.MkdirAll(base+checkpoint.CheckpointChecksumDirForBackup, 0o755)

	cipher := &backuppb.CipherInfo{
		CipherType: encryptionpb.EncryptionMethod_AES256_CTR,
		CipherKey:  []byte("01234567890123456789012345678901"),
	}
	checkpointRunner, err := checkpoint.StartCheckpointBackupRunnerForTest(
		ctx, s, cipher, 5*time.Second, NewMockTimer(10, 10))
	require.NoError(t, err)

	data := map[string]struct {
		StartKey string
		EndKey   string
		Name     string
		Name2    string
	}{
		"a": {
			StartKey: "a",
			EndKey:   "b",
			Name:     "c",
			Name2:    "d",
		},
		"A": {
			StartKey: "A",
			EndKey:   "B",
			Name:     "C",
			Name2:    "D",
		},
		"1": {
			StartKey: "1",
			EndKey:   "2",
			Name:     "3",
			Name2:    "4",
		},
	}

	data2 := map[string]struct {
		StartKey string
		EndKey   string
		Name     string
		Name2    string
	}{
		"+": {
			StartKey: "+",
			EndKey:   "-",
			Name:     "*",
			Name2:    "/",
		},
	}

	for _, d := range data {
		err = checkpoint.AppendForBackup(ctx, checkpointRunner, []byte(d.StartKey), []byte(d.EndKey), []*backuppb.File{
			{Name: d.Name},
			{Name: d.Name2},
		})
		require.NoError(t, err)
	}

	checkpointRunner.FlushChecksum(ctx, 1, 1, 1, 1)
	checkpointRunner.FlushChecksum(ctx, 2, 2, 2, 2)
	checkpointRunner.FlushChecksum(ctx, 3, 3, 3, 3)
	checkpointRunner.FlushChecksum(ctx, 4, 4, 4, 4)

	for _, d := range data2 {
		err = checkpoint.AppendForBackup(ctx, checkpointRunner, []byte(d.StartKey), []byte(d.EndKey), []*backuppb.File{
			{Name: d.Name},
			{Name: d.Name2},
		})
		require.NoError(t, err)
	}

	checkpointRunner.WaitForFinish(ctx, true)

	checker := func(groupKey string, resp checkpoint.BackupValueType) error {
		require.NotNil(t, resp)
		d, ok := data[string(resp.StartKey)]
		if !ok {
			d, ok = data2[string(resp.StartKey)]
			require.True(t, ok)
		}
		require.Equal(t, d.StartKey, string(resp.StartKey))
		require.Equal(t, d.EndKey, string(resp.EndKey))
		require.Equal(t, d.Name, resp.Files[0].Name)
		require.Equal(t, d.Name2, resp.Files[1].Name)
		return nil
	}

	_, err = checkpoint.WalkCheckpointFileForBackup(ctx, s, cipher, checker)
	require.NoError(t, err)

	checkpointMeta := &checkpoint.CheckpointMetadataForBackup{
		ConfigHash: []byte("123456"),
		BackupTS:   123456,
	}

	err = checkpoint.SaveCheckpointMetadata(ctx, s, checkpointMeta)
	require.NoError(t, err)
	meta, err := checkpoint.LoadCheckpointMetadata(ctx, s)
	require.NoError(t, err)

	var i int64
	for i = 1; i <= 4; i++ {
		require.Equal(t, meta.CheckpointChecksum[i].Crc64xor, uint64(i))
	}
}

func TestCheckpointRestoreRunnerOnStorage(t *testing.T) {
	base := t.TempDir()
	s, err := storage.NewLocalStorage(base)
	require.NoError(t, err)
	snapshotMetaManager := checkpoint.NewSnapshotStorageMetaManager(s, nil, 1, "snapshot", 1)
	defer snapshotMetaManager.Close()
	testCheckpointRestoreRunner(t, snapshotMetaManager)
}

func TestCheckpointRestoreRunnerOnTable(t *testing.T) {
	s := utiltest.CreateRestoreSchemaSuite(t)
	g := gluetidb.New()
	snapshotMetaManager, err := checkpoint.NewSnapshotTableMetaManager(g, s.Mock.Domain, checkpoint.SnapshotRestoreCheckpointDatabaseName, 1)
	require.NoError(t, err)
	defer snapshotMetaManager.Close()
	testCheckpointRestoreRunner(t, snapshotMetaManager)
}

func testCheckpointRestoreRunner(
	t *testing.T,
	snapshotMetaManager checkpoint.SnapshotMetaManagerT,
) {
	ctx := context.Background()

	err := snapshotMetaManager.SaveCheckpointMetadata(ctx, &checkpoint.CheckpointMetadataForSnapshotRestore{})
	require.NoError(t, err)
	checkpointRunner, err := checkpoint.StartCheckpointRestoreRunnerForTest(ctx, 5*time.Second, 3*time.Second, snapshotMetaManager)
	require.NoError(t, err)

	data := map[string]struct {
		RangeKey string
		Name     string
		Name2    string
	}{
		"a": {
			RangeKey: "a",
		},
		"A": {
			RangeKey: "A",
		},
		"1": {
			RangeKey: "1",
		},
	}

	data2 := map[string]struct {
		RangeKey string
		Name     string
		Name2    string
	}{
		"+": {
			RangeKey: "+",
		},
	}

	for _, d := range data {
		err = checkpoint.AppendRangesForRestore(ctx, checkpointRunner, checkpoint.NewCheckpointRangeKeyItem(1, d.RangeKey))
		require.NoError(t, err)
	}

	checkpointRunner.FlushChecksum(ctx, 1, 1, 1, 1)
	checkpointRunner.FlushChecksum(ctx, 2, 2, 2, 2)
	checkpointRunner.FlushChecksum(ctx, 3, 3, 3, 3)
	checkpointRunner.FlushChecksum(ctx, 4, 4, 4, 4)

	for _, d := range data2 {
		err = checkpoint.AppendRangesForRestore(ctx, checkpointRunner, checkpoint.NewCheckpointRangeKeyItem(2, d.RangeKey))
		require.NoError(t, err)
	}

	checkpointRunner.WaitForFinish(ctx, true)

	require.NoError(t, err)
	respCount := 0
	checker := func(tableID int64, resp checkpoint.RestoreValueType) error {
		require.NotNil(t, resp)
		d, ok := data[resp.RangeKey]
		if !ok {
			d, ok = data2[resp.RangeKey]
			require.Equal(t, tableID, int64(2))
			require.True(t, ok)
		} else {
			require.Equal(t, tableID, int64(1))
		}
		require.Equal(t, d.RangeKey, resp.RangeKey)
		respCount += 1
		return nil
	}

	_, err = snapshotMetaManager.LoadCheckpointData(ctx, checker)
	require.NoError(t, err)
	require.Equal(t, 4, respCount)

	checksum, _, err := snapshotMetaManager.LoadCheckpointChecksum(ctx)
	require.NoError(t, err)

	var i int64
	for i = 1; i <= 4; i++ {
		require.Equal(t, checksum[i].Crc64xor, uint64(i))
	}

	err = snapshotMetaManager.RemoveCheckpointData(ctx)
	require.NoError(t, err)

	exists, err := snapshotMetaManager.ExistsCheckpointMetadata(ctx)
	require.NoError(t, err)
	require.False(t, exists)
}

func TestCheckpointRunnerRetryOnStorage(t *testing.T) {
	base := t.TempDir()
	s, err := storage.NewLocalStorage(base)
	require.NoError(t, err)
	snapshotMetaManager := checkpoint.NewSnapshotStorageMetaManager(s, nil, 1, "snapshot", 1)
	defer snapshotMetaManager.Close()
	testCheckpointRunnerRetry(t, snapshotMetaManager)
}

func TestCheckpointRunnerRetryOnTable(t *testing.T) {
	s := utiltest.CreateRestoreSchemaSuite(t)
	g := gluetidb.New()
	snapshotMetaManager, err := checkpoint.NewSnapshotTableMetaManager(g, s.Mock.Domain, checkpoint.SnapshotRestoreCheckpointDatabaseName, 1)
	require.NoError(t, err)
	defer snapshotMetaManager.Close()
	testCheckpointRunnerRetry(t, snapshotMetaManager)
}

func testCheckpointRunnerRetry(
	t *testing.T,
	snapshotMetaManager checkpoint.SnapshotMetaManagerT,
) {
	ctx := context.Background()

	err := snapshotMetaManager.SaveCheckpointMetadata(ctx, &checkpoint.CheckpointMetadataForSnapshotRestore{})
	require.NoError(t, err)
	checkpointRunner, err := checkpoint.StartCheckpointRestoreRunnerForTest(ctx, 100*time.Millisecond, 300*time.Millisecond, snapshotMetaManager)
	require.NoError(t, err)

	err = failpoint.Enable("github.com/pingcap/tidb/br/pkg/checkpoint/failed-after-checkpoint-flushes", "return(true)")
	require.NoError(t, err)
	defer func() {
		err = failpoint.Disable("github.com/pingcap/tidb/br/pkg/checkpoint/failed-after-checkpoint-flushes")
		require.NoError(t, err)
	}()
	err = checkpoint.AppendRangesForRestore(ctx, checkpointRunner, checkpoint.NewCheckpointRangeKeyItem(1, "123"))
	require.NoError(t, err)
	err = checkpoint.AppendRangesForRestore(ctx, checkpointRunner, checkpoint.NewCheckpointRangeKeyItem(2, "456"))
	require.NoError(t, err)
	err = checkpointRunner.FlushChecksum(ctx, 1, 1, 1, 1)
	require.NoError(t, err)
	err = checkpointRunner.FlushChecksum(ctx, 2, 2, 2, 2)
	time.Sleep(time.Second)
	err = failpoint.Disable("github.com/pingcap/tidb/br/pkg/checkpoint/failed-after-checkpoint-flushes")
	require.NoError(t, err)
	err = checkpoint.AppendRangesForRestore(ctx, checkpointRunner, checkpoint.NewCheckpointRangeKeyItem(3, "789"))
	require.NoError(t, err)
	err = checkpointRunner.FlushChecksum(ctx, 3, 3, 3, 3)
	require.NoError(t, err)
	checkpointRunner.WaitForFinish(ctx, true)

	recordSet := make(map[string]int)
	_, err = snapshotMetaManager.LoadCheckpointData(ctx, func(tableID int64, v checkpoint.RestoreValueType) error {
		recordSet[fmt.Sprintf("%d_%s", tableID, v.RangeKey)] += 1
		return nil
	})
	require.NoError(t, err)
	require.LessOrEqual(t, 1, recordSet["1_123"])
	require.LessOrEqual(t, 1, recordSet["2_456"])
	require.LessOrEqual(t, 1, recordSet["3_789"])
	items, _, err := snapshotMetaManager.LoadCheckpointChecksum(ctx)
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("%d_%d_%d", items[1].Crc64xor, items[1].TotalBytes, items[1].TotalKvs), "1_1_1")
	require.Equal(t, fmt.Sprintf("%d_%d_%d", items[2].Crc64xor, items[2].TotalBytes, items[2].TotalKvs), "2_2_2")
	require.Equal(t, fmt.Sprintf("%d_%d_%d", items[3].Crc64xor, items[3].TotalBytes, items[3].TotalKvs), "3_3_3")
}

func TestCheckpointRunnerNoRetryOnStorage(t *testing.T) {
	base := t.TempDir()
	s, err := storage.NewLocalStorage(base)
	require.NoError(t, err)
	snapshotMetaManager := checkpoint.NewSnapshotStorageMetaManager(s, nil, 1, "snapshot", 1)
	defer snapshotMetaManager.Close()
	testCheckpointRunnerNoRetry(t, snapshotMetaManager)
}

func TestCheckpointRunnerNoRetryOnTable(t *testing.T) {
	s := utiltest.CreateRestoreSchemaSuite(t)
	g := gluetidb.New()
	snapshotMetaManager, err := checkpoint.NewSnapshotTableMetaManager(g, s.Mock.Domain, checkpoint.SnapshotRestoreCheckpointDatabaseName, 1)
	require.NoError(t, err)
	defer snapshotMetaManager.Close()
	testCheckpointRunnerNoRetry(t, snapshotMetaManager)
}

func testCheckpointRunnerNoRetry(
	t *testing.T,
	snapshotMetaManager checkpoint.SnapshotMetaManagerT,
) {
	ctx := context.Background()

	err := snapshotMetaManager.SaveCheckpointMetadata(ctx, &checkpoint.CheckpointMetadataForSnapshotRestore{})
	require.NoError(t, err)
	checkpointRunner, err := checkpoint.StartCheckpointRestoreRunnerForTest(ctx, 100*time.Millisecond, 300*time.Millisecond, snapshotMetaManager)
	require.NoError(t, err)

	err = checkpoint.AppendRangesForRestore(ctx, checkpointRunner, checkpoint.NewCheckpointRangeKeyItem(1, "123"))
	require.NoError(t, err)
	err = checkpoint.AppendRangesForRestore(ctx, checkpointRunner, checkpoint.NewCheckpointRangeKeyItem(2, "456"))
	require.NoError(t, err)
	err = checkpointRunner.FlushChecksum(ctx, 1, 1, 1, 1)
	require.NoError(t, err)
	err = checkpointRunner.FlushChecksum(ctx, 2, 2, 2, 2)
	require.NoError(t, err)
	time.Sleep(time.Second)
	checkpointRunner.WaitForFinish(ctx, true)

	require.NoError(t, err)
	recordSet := make(map[string]int)
	_, err = snapshotMetaManager.LoadCheckpointData(ctx, func(tableID int64, v checkpoint.RestoreValueType) error {
		recordSet[fmt.Sprintf("%d_%s", tableID, v.RangeKey)] += 1
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, recordSet["1_123"])
	require.Equal(t, 1, recordSet["2_456"])
	items, _, err := snapshotMetaManager.LoadCheckpointChecksum(ctx)
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("%d_%d_%d", items[1].Crc64xor, items[1].TotalBytes, items[1].TotalKvs), "1_1_1")
	require.Equal(t, fmt.Sprintf("%d_%d_%d", items[2].Crc64xor, items[2].TotalBytes, items[2].TotalKvs), "2_2_2")
}

func TestCheckpointLogRestoreRunnerOnStorage(t *testing.T) {
	base := t.TempDir()
	s, err := storage.NewLocalStorage(base)
	require.NoError(t, err)
	logMetaManager := checkpoint.NewLogStorageMetaManager(s, nil, 1, "log", 1)
	defer logMetaManager.Close()
	testCheckpointLogRestoreRunner(t, logMetaManager)
}

func TestCheckpointLogRestoreRunnerOnTable(t *testing.T) {
	s := utiltest.CreateRestoreSchemaSuite(t)
	g := gluetidb.New()
	logMetaManager, err := checkpoint.NewLogTableMetaManager(g, s.Mock.Domain, checkpoint.LogRestoreCheckpointDatabaseName, 1)
	require.NoError(t, err)
	defer logMetaManager.Close()
	testCheckpointLogRestoreRunner(t, logMetaManager)
}

func testCheckpointLogRestoreRunner(
	t *testing.T,
	logMetaManager checkpoint.LogMetaManagerT,
) {
	ctx := context.Background()

	err := logMetaManager.SaveCheckpointMetadata(ctx, &checkpoint.CheckpointMetadataForLogRestore{})
	require.NoError(t, err)
	checkpointRunner, err := checkpoint.StartCheckpointLogRestoreRunnerForTest(ctx, 5*time.Second, logMetaManager)
	require.NoError(t, err)

	data := map[string]map[int][]struct {
		table int64
		foff  int
	}{
		"a": {
			0: {{1, 0}, {2, 1}},
			1: {{1, 0}},
		},
		"A": {
			0: {{3, 1}},
		},
	}

	data2 := map[string]map[int][]struct {
		table int64
		foff  int
	}{
		"+": {
			1: {{1, 0}},
		},
	}

	for k, d := range data {
		for g, fs := range d {
			for _, f := range fs {
				err = checkpoint.AppendRangeForLogRestore(ctx, checkpointRunner, k, f.table, g, f.foff)
				require.NoError(t, err)
			}
		}
	}

	for k, d := range data2 {
		for g, fs := range d {
			for _, f := range fs {
				err = checkpoint.AppendRangeForLogRestore(ctx, checkpointRunner, k, f.table, g, f.foff)
				require.NoError(t, err)
			}
		}
	}

	checkpointRunner.WaitForFinish(ctx, true)

	require.NoError(t, err)
	respCount := 0
	checker := func(metaKey string, resp checkpoint.LogRestoreValueMarshaled) error {
		require.NotNil(t, resp)
		d, ok := data[metaKey]
		if !ok {
			d, ok = data2[metaKey]
			require.True(t, ok)
		}
		fs, ok := d[resp.Goff]
		require.True(t, ok)
		for _, f := range fs {
			foffs, exists := resp.Foffs[f.table]
			if !exists {
				continue
			}
			if slices.Contains(foffs, f.foff) {
				respCount += 1
				return nil
			}
		}
		require.FailNow(t, "not found in the original data")
		return nil
	}

	_, err = logMetaManager.LoadCheckpointData(ctx, checker)
	require.NoError(t, err)
	require.Equal(t, 4, respCount)

	err = logMetaManager.RemoveCheckpointData(ctx)
	require.NoError(t, err)

	exists, err := logMetaManager.ExistsCheckpointMetadata(ctx)
	require.NoError(t, err)
	require.False(t, exists)
}

func getLockData(p, l int64) ([]byte, error) {
	lock := checkpoint.CheckpointLock{
		LockId:   oracle.ComposeTS(p, l),
		ExpireAt: p + 10,
	}
	return json.Marshal(lock)
}

func TestCheckpointRunnerLock(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	s, err := storage.NewLocalStorage(base)
	require.NoError(t, err)
	os.MkdirAll(base+checkpoint.CheckpointDataDirForBackup, 0o755)
	os.MkdirAll(base+checkpoint.CheckpointChecksumDirForBackup, 0o755)

	cipher := &backuppb.CipherInfo{
		CipherType: encryptionpb.EncryptionMethod_AES256_CTR,
		CipherKey:  []byte("01234567890123456789012345678901"),
	}

	data, err := getLockData(10, 20)
	require.NoError(t, err)
	err = s.WriteFile(ctx, checkpoint.CheckpointLockPathForBackup, data)
	require.NoError(t, err)

	_, err = checkpoint.StartCheckpointBackupRunnerForTest(ctx, s, cipher, 5*time.Second, NewMockTimer(10, 10))
	require.Error(t, err)

	runner, err := checkpoint.StartCheckpointBackupRunnerForTest(ctx, s, cipher, 5*time.Second, NewMockTimer(30, 10))
	require.NoError(t, err)

	_, err = checkpoint.StartCheckpointBackupRunnerForTest(ctx, s, cipher, 5*time.Second, NewMockTimer(40, 10))
	require.Error(t, err)

	runner.WaitForFinish(ctx, true)
}
