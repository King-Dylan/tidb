// Copyright 2024 PingCAP, Inc.
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

package notifier

import (
	"testing"

	"github.com/pingcap/tidb/pkg/meta/model"
	"github.com/pingcap/tidb/pkg/parser/ast"
	"github.com/stretchr/testify/require"
)

func TestEventString(t *testing.T) {
	// Create an Event object
	e := &SchemaChangeEvent{
		inner: &jsonSchemaChangeEvent{
			Tp: model.ActionAddColumn,
			TableInfo: &model.TableInfo{
				ID:   1,
				Name: ast.NewCIStr("Table1"),
			},
			AddedPartInfo: &model.PartitionInfo{
				Definitions: []model.PartitionDefinition{
					{ID: 2},
					{ID: 3},
				},
			},
			OldTableInfo: &model.TableInfo{
				ID:   4,
				Name: ast.NewCIStr("Table2"),
			},
			DroppedPartInfo: &model.PartitionInfo{
				Definitions: []model.PartitionDefinition{
					{ID: 5},
					{ID: 6},
				},
			},
			Columns: []*model.ColumnInfo{
				{ID: 7, Name: ast.NewCIStr("Column1")},
				{ID: 8, Name: ast.NewCIStr("Column2")},
			},
			Indexes: []*model.IndexInfo{
				{ID: 9, Name: ast.NewCIStr("Index1")},
				{ID: 10, Name: ast.NewCIStr("Index2")},
			},
		},
	}

	// Call the String method
	result := e.String()

	// Check the result
	expected := "(Event Type: add column, Table ID: 1, Table Name: Table1, Old Table ID: 4, Old Table Name: Table2," +
		" Partition ID: 2, Partition ID: 3, Dropped Partition ID: 5, Dropped Partition ID: 6, " +
		"Column ID: 7, Column Name: Column1, Column ID: 8, Column Name: Column2, " +
		"Index ID: 9, Index Name: Index1, Index ID: 10, Index Name: Index2)"
	require.Equal(t, expected, result)
}
