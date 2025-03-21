#!/bin/bash
#
# Copyright 2019 PingCAP, Inc.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euE

CUR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# Populate the mydumper source
DBPATH="$TEST_DIR/cpch.mydump"
CHUNK_COUNT=5
ROW_COUNT=1000

do_run_lightning() {
    run_lightning -d "$DBPATH" --enable-checkpoint=1 --config "$CUR/$1.toml"
}

verify_checkpoint_noop() {
    # After everything is done, there should be no longer new calls to WriteEngine/CloseAndRecv
    # (and thus `kill_lightning_after_one_chunk` will spare this final check)
    echo "******** Verify checkpoint no-op ********"
    do_run_lightning config
    run_sql 'SELECT count(i), sum(i) FROM cpch_tsr.tbl;'
    check_contains "count(i): $(($ROW_COUNT*$CHUNK_COUNT))"
    check_contains "sum(i): $(( $ROW_COUNT*$CHUNK_COUNT*(($CHUNK_COUNT+2)*$ROW_COUNT + 1)/2 ))"
    run_sql 'SELECT count(*) FROM `tidb_lightning_checkpoint_test_cpch.1234567890.bak`.table_v10 WHERE status >= 200'
    check_contains "count(*): 1"
}

mkdir -p $DBPATH
echo 'CREATE DATABASE cpch_tsr;' > "$DBPATH/cpch_tsr-schema-create.sql"
echo 'CREATE TABLE tbl(i BIGINT UNSIGNED PRIMARY KEY);' > "$DBPATH/cpch_tsr.tbl-schema.sql"
for i in $(seq "$CHUNK_COUNT"); do
    rm -f "$DBPATH/cpch_tsr.tbl.$i.sql"
    for j in $(seq "$ROW_COUNT"); do
        # the values run from ($ROW_COUNT + 1) to $CHUNK_COUNT*($ROW_COUNT + 1).
        echo "INSERT INTO tbl VALUES($(($i*$ROW_COUNT+$j)));" >> "$DBPATH/cpch_tsr.tbl.$i.sql"
    done
done

PKG="github.com/pingcap/tidb/lightning/pkg"
export GO_FAILPOINTS="github.com/pingcap/tidb/pkg/lightning/backend/local/orphanWriterGoRoutine=return();$PKG/importer/orphanWriterGoRoutine=return();$PKG/server/orphanWriterGoRoutine=return()"
# test won't panic
do_run_lightning config

# Set the failpoint to kill the lightning instance as soon as
# one file (after writing totally $ROW_COUNT rows) is imported.
# If checkpoint does work, this should kill exactly $CHUNK_COUNT instances of lightnings.
TASKID_FAILPOINTS="github.com/pingcap/tidb/lightning/pkg/server/SetTaskID=return(1234567890)"
export GO_FAILPOINTS="$TASKID_FAILPOINTS;github.com/pingcap/tidb/lightning/pkg/importer/FailIfImportedChunk=return"

# Start importing the tables.
run_sql 'DROP DATABASE IF EXISTS cpch_tsr'
run_sql 'DROP DATABASE IF EXISTS tidb_lightning_checkpoint_test_cpch'
run_sql 'DROP DATABASE IF EXISTS `tidb_lightning_checkpoint_test_cpch.1234567890.bak`'

set +e
for i in $(seq "$CHUNK_COUNT"); do
    echo "******** Importing Chunk Now (step $i/$CHUNK_COUNT) ********"
    do_run_lightning config 2> /dev/null
    [ $? -ne 0 ] || exit 1
done
set -e

verify_checkpoint_noop

# Next, test kill lightning via signal mechanism
run_sql 'DROP DATABASE IF EXISTS cpch_tsr'
run_sql 'DROP DATABASE IF EXISTS tidb_lightning_checkpoint_test_cpch'
run_sql 'DROP DATABASE IF EXISTS `tidb_lightning_checkpoint_test_cpch.1234567890.bak`'

# Set the failpoint to kill the lightning instance as soon as one chunk is imported, via signal mechanism
# If checkpoint does work, this should only kill $CHUNK_COUNT instances of lightnings.
export GO_FAILPOINTS="$TASKID_FAILPOINTS;github.com/pingcap/tidb/lightning/pkg/importer/KillIfImportedChunk=return"

for i in $(seq "$CHUNK_COUNT"); do
    echo "******** Importing Chunk Now (step $i/$CHUNK_COUNT) ********"
    do_run_lightning config
done

verify_checkpoint_noop

# Repeat, but using the file checkpoint
run_sql 'DROP DATABASE IF EXISTS cpch_tsr'
run_sql 'DROP DATABASE IF EXISTS tidb_lightning_checkpoint_test_cpch'
rm -f "$TEST_DIR"/cpch.pb*

# Set the failpoint to kill the lightning instance as soon as one chunk is imported
# If checkpoint does work, this should only kill $CHUNK_COUNT instances of lightnings.
export GO_FAILPOINTS="$TASKID_FAILPOINTS;github.com/pingcap/tidb/lightning/pkg/importer/FailIfImportedChunk=return"
set +e
for i in $(seq "$CHUNK_COUNT"); do
    echo "******** Importing Chunk using File checkpoint Now (step $i/$CHUNK_COUNT) ********"
    do_run_lightning file 2> /dev/null
    [ $? -ne 0 ] || exit 1
done
set -e

echo "******** Verify File checkpoint no-op ********"
do_run_lightning file
run_sql 'SELECT count(i), sum(i) FROM cpch_tsr.tbl;'
check_contains "count(i): $(($ROW_COUNT*$CHUNK_COUNT))"
check_contains "sum(i): $(( $ROW_COUNT*$CHUNK_COUNT*(($CHUNK_COUNT+2)*$ROW_COUNT + 1)/2 ))"
[ ! -e "$TEST_DIR/cpch.pb" ]
[ -e "$TEST_DIR/cpch.pb.1234567890.bak" ]

## default auto analyze tick is 3s
sleep 3
run_sql "SHOW STATS_META WHERE Table_name = 'tbl';"
check_contains "Row_count: 5000"
## TODO: Use failpoint to control the auto analyze tick
check_contains "Modify_count: 5000"

