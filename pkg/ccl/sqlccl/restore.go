// Copyright 2016 The Cockroach Authors.
//
// Licensed as a CockroachDB Enterprise file under the Cockroach Community
// License (the "License"); you may not use this file except in compliance with
// the License. You may obtain a copy of the License at
//
//     https://github.com/cockroachdb/cockroach/blob/master/LICENSE

package sqlccl

import (
	"math"
	"runtime"
	"sort"
	"sync/atomic"

	opentracing "github.com/opentracing/opentracing-go"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
	"golang.org/x/sync/errgroup"

	"github.com/cockroachdb/cockroach/pkg/ccl/storageccl"
	"github.com/cockroachdb/cockroach/pkg/ccl/utilccl"
	"github.com/cockroachdb/cockroach/pkg/ccl/utilccl/intervalccl"
	"github.com/cockroachdb/cockroach/pkg/gossip"
	"github.com/cockroachdb/cockroach/pkg/internal/client"
	"github.com/cockroachdb/cockroach/pkg/keys"
	"github.com/cockroachdb/cockroach/pkg/roachpb"
	"github.com/cockroachdb/cockroach/pkg/settings/cluster"
	"github.com/cockroachdb/cockroach/pkg/sql"
	"github.com/cockroachdb/cockroach/pkg/sql/jobs"
	"github.com/cockroachdb/cockroach/pkg/sql/parser"
	"github.com/cockroachdb/cockroach/pkg/sql/privilege"
	"github.com/cockroachdb/cockroach/pkg/sql/sqlbase"
	"github.com/cockroachdb/cockroach/pkg/util/hlc"
	"github.com/cockroachdb/cockroach/pkg/util/humanizeutil"
	"github.com/cockroachdb/cockroach/pkg/util/interval"
	"github.com/cockroachdb/cockroach/pkg/util/log"
	"github.com/cockroachdb/cockroach/pkg/util/protoutil"
	"github.com/cockroachdb/cockroach/pkg/util/syncutil"
	"github.com/cockroachdb/cockroach/pkg/util/timeutil"
	"github.com/cockroachdb/cockroach/pkg/util/tracing"
)

type tableRewriteMap map[sqlbase.ID]*jobs.RestoreDetails_TableRewrite

const (
	restoreOptIntoDB         = "into_db"
	restoreOptSkipMissingFKs = "skip_missing_foreign_keys"
)

var restoreOptionExpectValues = map[string]bool{
	restoreOptIntoDB:         true,
	restoreOptSkipMissingFKs: false,
}

func loadBackupDescs(
	ctx context.Context, uris []string, settings *cluster.Settings,
) ([]BackupDescriptor, error) {
	backupDescs := make([]BackupDescriptor, len(uris))

	for i, uri := range uris {
		desc, err := ReadBackupDescriptorFromURI(ctx, uri, settings)
		if err != nil {
			return nil, errors.Wrap(err, "failed to read backup descriptor")
		}
		backupDescs[i] = desc
	}
	if len(backupDescs) == 0 {
		return nil, errors.Errorf("no backups found")
	}
	return backupDescs, nil
}

func selectTargets(
	p sql.PlanHookState, backupDescs []BackupDescriptor, targets parser.TargetList,
) ([]sqlbase.Descriptor, []*sqlbase.DatabaseDescriptor, error) {
	sessionDatabase := p.EvalContext().Database
	lastBackupDesc := backupDescs[len(backupDescs)-1]
	sqlDescs, dbs, err := descriptorsMatchingTargets(sessionDatabase, lastBackupDesc.Descriptors, targets)
	if err != nil {
		return nil, nil, err
	}

	seenTable := false
	for _, desc := range sqlDescs {
		if desc.GetTable() != nil {
			seenTable = true
		}
	}
	if !seenTable {
		return nil, nil, errors.Errorf("no tables found: %s", parser.AsString(targets))
	}

	return sqlDescs, dbs, nil
}

// allocateTableRewrites determines the new ID and parentID (a "TableRewrite")
// for each table in sqlDescs and returns a mapping from old ID to said
// TableRewrite. It first validates that the provided sqlDescs can be restored
// into their original database (or the database specified in opst) to avoid
// leaking table IDs if we can be sure the restore would fail.
func allocateTableRewrites(
	ctx context.Context,
	p sql.PlanHookState,
	sqlDescs []sqlbase.Descriptor,
	restoreDBs []*sqlbase.DatabaseDescriptor,
	opts map[string]string,
) (tableRewriteMap, error) {
	tableRewrites := make(tableRewriteMap)
	_, renaming := opts[restoreOptIntoDB]

	restoreDBNames := make(map[string]*sqlbase.DatabaseDescriptor, len(restoreDBs))
	for _, db := range restoreDBs {
		restoreDBNames[db.Name] = db
	}

	if len(restoreDBNames) > 0 && renaming {
		return nil, errors.Errorf("cannot use %q option when restoring database(s)", restoreOptIntoDB)
	}

	databasesByID := make(map[sqlbase.ID]*sqlbase.DatabaseDescriptor)
	tablesByID := make(map[sqlbase.ID]*sqlbase.TableDescriptor)
	for _, desc := range sqlDescs {
		if dbDesc := desc.GetDatabase(); dbDesc != nil {
			databasesByID[dbDesc.ID] = dbDesc
		} else if tableDesc := desc.GetTable(); tableDesc != nil {
			tablesByID[tableDesc.ID] = tableDesc
		}
	}

	// The logic at the end of this function leaks table IDs, so fail fast if
	// we can be certain the restore will fail.

	// Fail fast if the tables to restore are incompatible with the specified
	// options.
	for _, table := range tablesByID {
		if renaming && table.IsView() {
			return nil, errors.Errorf("cannot restore view when using %q option", restoreOptIntoDB)
		}

		if err := table.ForeachNonDropIndex(func(index *sqlbase.IndexDescriptor) error {
			if index.ForeignKey.IsSet() {
				to := index.ForeignKey.Table
				if _, ok := tablesByID[to]; !ok {
					if _, ok := opts[restoreOptSkipMissingFKs]; ok {
						index.ForeignKey = sqlbase.ForeignKeyReference{}
					} else {
						return errors.Errorf(
							"cannot restore table %q without referenced table %d (or %q option)",
							table.Name, to, restoreOptSkipMissingFKs,
						)
					}
				}
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}

	needsNewParentIDs := make(map[string][]sqlbase.ID)

	// Fail fast if the necessary databases don't exist or are otherwise
	// incompatible with this restore.
	if err := p.ExecCfg().DB.Txn(ctx, func(ctx context.Context, txn *client.Txn) error {
		// Check that any DBs being restored do _not_ exist.
		for name := range restoreDBNames {
			existingDatabaseID, err := txn.Get(ctx, sqlbase.MakeNameMetadataKey(keys.RootNamespaceID, name))
			if err != nil {
				return err
			}
			if existingDatabaseID.Value != nil {
				return errors.Errorf("database %q already exists", name)
			}
		}

		for _, table := range tablesByID {
			var targetDB string
			if override, ok := opts[restoreOptIntoDB]; ok {
				targetDB = override
			} else {
				database, ok := databasesByID[table.ParentID]
				if !ok {
					return errors.Errorf("no database with ID %d in backup for table %q",
						table.ParentID, table.Name)
				}
				targetDB = database.Name
			}

			if _, ok := restoreDBNames[targetDB]; ok {
				needsNewParentIDs[targetDB] = append(needsNewParentIDs[targetDB], table.ID)
			} else {
				var parentID sqlbase.ID
				{
					existingDatabaseID, err := txn.Get(ctx, sqlbase.MakeNameMetadataKey(keys.RootNamespaceID, targetDB))
					if err != nil {
						return err
					}
					if existingDatabaseID.Value == nil {
						return errors.Errorf("a database named %q needs to exist to restore table %q",
							targetDB, table.Name)
					}

					newParentID, err := existingDatabaseID.Value.GetInt()
					if err != nil {
						return err
					}
					parentID = sqlbase.ID(newParentID)
				}

				// Check that the table name is _not_ in use.
				// This would fail the CPut later anyway, but this yields a prettier error.
				{
					nameKey := sqlbase.MakeNameMetadataKey(parentID, table.Name)
					res, err := txn.Get(ctx, nameKey)
					if err != nil {
						return err
					}
					if res.Exists() {
						return sqlbase.NewRelationAlreadyExistsError(table.Name)
					}
				}

				// Check privileges. These will be checked again in the transaction
				// that actually writes the new table descriptors.
				{
					parentDB, err := sqlbase.GetDatabaseDescFromID(ctx, txn, parentID)
					if err != nil {
						return errors.Wrapf(err, "failed to lookup parent DB %d", parentID)
					}

					if err := p.CheckPrivilege(parentDB, privilege.CREATE); err != nil {
						return err
					}
				}
				// Create the table rewrite with the new parent ID. We've done all the
				// up-front validation that we can.
				tableRewrites[table.ID] = &jobs.RestoreDetails_TableRewrite{ParentID: parentID}
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}

	// Allocate new IDs for each database and table.
	//
	// NB: we do this in a standalone transaction, not one that covers the
	// entire restore since restarts would be terrible (and our bulk import
	// primitive are non-transactional), but this does mean if something fails
	// during restore we've "leaked" the IDs, in that the generator will have
	// been incremented.
	//
	// NB: The ordering of the new IDs must be the same as the old ones,
	// otherwise the keys may sort differently after they're rekeyed. We could
	// handle this by chunking the AddSSTable calls more finely in Import, but
	// it would be a big performance hit.

	for _, db := range restoreDBs {
		newID, err := sql.GenerateUniqueDescID(ctx, p.ExecCfg().DB)
		if err != nil {
			return nil, err
		}
		tableRewrites[db.ID] = &jobs.RestoreDetails_TableRewrite{TableID: newID}
		for _, tableID := range needsNewParentIDs[db.Name] {
			tableRewrites[tableID] = &jobs.RestoreDetails_TableRewrite{ParentID: newID}
		}
	}

	tables := make([]*sqlbase.TableDescriptor, 0, len(tablesByID))
	for _, table := range tablesByID {
		tables = append(tables, table)
	}
	sort.Sort(sqlbase.TableDescriptors(tables))
	for _, table := range tables {
		newTableID, err := sql.GenerateUniqueDescID(ctx, p.ExecCfg().DB)
		if err != nil {
			return nil, err
		}
		tableRewrites[table.ID].TableID = newTableID
	}

	return tableRewrites, nil
}

// rewriteTableDescs mutates tables to match the ID and privilege specified in
// tableRewrites, as well as adjusting cross-table references to use the new
// IDs.
func rewriteTableDescs(tables []*sqlbase.TableDescriptor, tableRewrites tableRewriteMap) error {
	for _, table := range tables {
		tableRewrite, ok := tableRewrites[table.ID]
		if !ok {
			return errors.Errorf("missing table rewrite for table %d", table.ID)
		}
		table.ID = tableRewrite.TableID
		table.ParentID = tableRewrite.ParentID

		if err := table.ForeachNonDropIndex(func(index *sqlbase.IndexDescriptor) error {
			// Verify that for any interleaved index being restored, the interleave
			// parent is also being restored. Otherwise, the interleave entries in the
			// restored IndexDescriptors won't have anything to point to.
			// TODO(dan): It seems like this restriction could be lifted by restoring
			// stub TableDescriptors for the missing interleave parents.
			for j, a := range index.Interleave.Ancestors {
				ancestorRewrite, ok := tableRewrites[a.TableID]
				if !ok {
					return errors.Errorf(
						"cannot restore table %q without interleave parent %d", table.Name, a.TableID,
					)
				}
				index.Interleave.Ancestors[j].TableID = ancestorRewrite.TableID
			}
			for j, c := range index.InterleavedBy {
				childRewrite, ok := tableRewrites[c.Table]
				if !ok {
					return errors.Errorf(
						"cannot restore table %q without interleave child table %d", table.Name, c.Table,
					)
				}
				index.InterleavedBy[j].Table = childRewrite.TableID
			}

			if index.ForeignKey.IsSet() {
				to := index.ForeignKey.Table
				if indexRewrite, ok := tableRewrites[to]; ok {
					index.ForeignKey.Table = indexRewrite.TableID
				}
				// If indexRewrite doesn't exist, either the user has specified
				// restoreOptSkipMissingFKs, or we've already errored in
				// allocateTableRewrites. Move on.

				// TODO(dt): if there is an existing (i.e. non-restoring) table with
				// a db and name matching the one the FK pointed to at backup, should
				// we update the FK to point to it?
			}

			origRefs := index.ReferencedBy
			index.ReferencedBy = nil
			for _, ref := range origRefs {
				if refRewrite, ok := tableRewrites[ref.Table]; ok {
					ref.Table = refRewrite.TableID
					index.ReferencedBy = append(index.ReferencedBy, ref)
				}
			}
			return nil
		}); err != nil {
			return err
		}

		for i, dest := range table.DependsOn {
			if depRewrite, ok := tableRewrites[dest]; ok {
				table.DependsOn[i] = depRewrite.TableID
			} else {
				return errors.Errorf(
					"cannot restore %q without restoring referenced table %d in same operation",
					table.Name, dest)
			}
		}
		origRefs := table.DependedOnBy
		table.DependedOnBy = nil
		for _, ref := range origRefs {
			if refRewrite, ok := tableRewrites[ref.ID]; ok {
				ref.ID = refRewrite.TableID
				table.DependedOnBy = append(table.DependedOnBy, ref)
			}
		}

		// since this is a "new" table in eyes of new cluster, any leftover change
		// lease is obviously bogus (plus the nodeID is relative to backup cluster).
		table.Lease = nil
	}
	return nil
}

type intervalSpan roachpb.Span

var _ interval.Interface = intervalSpan{}

// ID is part of `interval.Interface` but unused in makeImportSpans.
func (ie intervalSpan) ID() uintptr { return 0 }

// Range is part of `interval.Interface`.
func (ie intervalSpan) Range() interval.Range {
	return interval.Range{Start: []byte(ie.Key), End: []byte(ie.EndKey)}
}

type importEntryType int

const (
	backupSpan importEntryType = iota
	backupFile
	tableSpan
	completedSpan
	request
)

type importEntry struct {
	roachpb.Span
	entryType importEntryType

	// Only set if entryType is backupSpan
	backup BackupDescriptor

	// Only set if entryType is backupFile
	dir  roachpb.ExportStorage
	file BackupDescriptor_File

	// Only set if entryType is request
	files []roachpb.ImportRequest_File
}

// makeImportSpans pivots the backups, which are grouped by time, into
// spans for import, which are grouped by keyrange.
//
// The core logic of this is in OverlapCoveringMerge, which accepts sets of
// non-overlapping key ranges (aka coverings) each with a payload, and returns
// them aligned with the payloads in the same order as in the input.
//
// Example (input):
// - [A, C) backup t0 to t1 -> /file1
// - [C, D) backup t0 to t1 -> /file2
// - [A, B) backup t1 to t2 -> /file3
// - [B, C) backup t1 to t2 -> /file4
// - [C, D) backup t1 to t2 -> /file5
// - [B, D) requested table data to be restored
//
// Example (output):
// - [A, B) -> /file1, /file3
// - [B, C) -> /file1, /file4, requested (note that file1 was split into two ranges)
// - [C, D) -> /file2, /file5, requested
//
// This would be turned into two Import spans, one restoring [B, C) out of
// /file1 and /file3, the other restoring [C, D) out of /file2 and /file5.
// Nothing is restored out of /file3 and only part of /file1 is used.
//
// NB: All grouping operates in the pre-rewrite keyspace, meaning the keyranges
// as they were backed up, not as they're being restored.
func makeImportSpans(
	tableSpans []roachpb.Span, backups []BackupDescriptor, lowWaterMark roachpb.Key,
) ([]importEntry, hlc.Timestamp, error) {
	// Put the covering for the already-completed spans into the
	// OverlapCoveringMerge input first. Payloads are returned in the same order
	// that they appear in the input; putting the completedSpan first means we'll
	// see it first when iterating over the output of OverlapCoveringMerge and
	// avoid doing unnecessary work.
	completedCovering := intervalccl.Covering{
		{
			Start:   []byte(keys.MinKey),
			End:     []byte(lowWaterMark),
			Payload: importEntry{entryType: completedSpan},
		},
	}

	// Put the merged table data covering into the OverlapCoveringMerge input
	// next.
	var tableSpanCovering intervalccl.Covering
	for _, span := range tableSpans {
		tableSpanCovering = append(tableSpanCovering, intervalccl.Range{
			Start: span.Key,
			End:   span.EndKey,
			Payload: importEntry{
				Span:      span,
				entryType: tableSpan,
			},
		})
	}

	backupCoverings := []intervalccl.Covering{completedCovering, tableSpanCovering}

	// Iterate over backups creating two coverings for each. First the spans
	// that were backed up, then the files in the backup. The latter is a subset
	// when some of the keyranges in the former didn't change since the previous
	// backup. These alternate (backup1 spans, backup1 files, backup2 spans,
	// backup2 files) so they will retain that alternation in the output of
	// OverlapCoveringMerge.
	var maxEndTime hlc.Timestamp
	for _, b := range backups {
		if maxEndTime.Less(b.EndTime) {
			maxEndTime = b.EndTime
		}

		var backupSpanCovering intervalccl.Covering
		for _, s := range b.Spans {
			backupSpanCovering = append(backupSpanCovering, intervalccl.Range{
				Start:   s.Key,
				End:     s.EndKey,
				Payload: importEntry{Span: s, entryType: backupSpan, backup: b},
			})
		}
		backupCoverings = append(backupCoverings, backupSpanCovering)
		var backupFileCovering intervalccl.Covering
		for _, f := range b.Files {
			backupFileCovering = append(backupFileCovering, intervalccl.Range{
				Start: f.Span.Key,
				End:   f.Span.EndKey,
				Payload: importEntry{
					Span:      f.Span,
					entryType: backupFile,
					dir:       b.Dir,
					file:      f,
				},
			})
		}
		backupCoverings = append(backupCoverings, backupFileCovering)
	}

	// Group ranges covered by backups with ones needed to restore the selected
	// tables. Note that this breaks intervals up as necessary to align them.
	// See the function godoc for details.
	importRanges := intervalccl.OverlapCoveringMerge(backupCoverings)

	// Translate the output of OverlapCoveringMerge into requests.
	var requestEntries []importEntry
rangeLoop:
	for _, importRange := range importRanges {
		needed := false
		var ts hlc.Timestamp
		var files []roachpb.ImportRequest_File
		payloads := importRange.Payload.([]interface{})
		for _, p := range payloads {
			ie := p.(importEntry)
			switch ie.entryType {
			case completedSpan:
				continue rangeLoop
			case tableSpan:
				needed = true
			case backupSpan:
				if ts != ie.backup.StartTime {
					return nil, hlc.Timestamp{}, errors.Errorf(
						"no backup covers time [%s,%s) for range [%s,%s) (or backups out of order)",
						ts, ie.backup.StartTime,
						roachpb.Key(importRange.Start), roachpb.Key(importRange.End))
				}
				ts = ie.backup.EndTime
			case backupFile:
				if len(ie.file.Path) > 0 {
					files = append(files, roachpb.ImportRequest_File{
						Dir:    ie.dir,
						Path:   ie.file.Path,
						Sha512: ie.file.Sha512,
					})
				}
			}
		}
		if needed {
			if ts != maxEndTime {
				return nil, hlc.Timestamp{}, errors.Errorf(
					"no backup covers time [%s,%s) for range [%s,%s) (or backups out of order)",
					ts, maxEndTime, roachpb.Key(importRange.Start), roachpb.Key(importRange.End))
			}
			// If needed is false, we have data backed up that is not necessary
			// for this restore. Skip it.
			requestEntries = append(requestEntries, importEntry{
				Span:      roachpb.Span{Key: importRange.Start, EndKey: importRange.End},
				entryType: request,
				files:     files,
			})
		}
	}
	return requestEntries, maxEndTime, nil
}

// splitAndScatter creates new ranges for importSpans and scatters replicas and
// leaseholders to be as evenly balanced as possible. It does this with some
// amount of parallelism but also staying as close to the order in importSpans
// as possible (the more out of order, the more work is done if a RESTORE job
// loses its lease and has to be restarted).
//
// At a high level, this is accomplished by splitting and scattering large
// "chunks" from the front of importEntries in one goroutine, each of which are
// in turn passed to one of many worker goroutines that split and scatter the
// individual entries.
//
// importEntries are sent to readyForImportCh as they are scattered, so letting
// that channel send block can be used for backpressure on the splits and
// scatters.
//
// TODO(dan): This logic is largely tuned by running BenchmarkRestore2TB. See if
// there's some way to test it without running an O(hour) long benchmark.
func splitAndScatter(
	restoreCtx context.Context,
	db *client.DB,
	kr *storageccl.KeyRewriter,
	numClusterNodes int,
	importSpans []importEntry,
	readyForImportCh chan<- importEntry,
) error {
	var span opentracing.Span
	ctx, span := tracing.ChildSpan(restoreCtx, "presplit-scatter")
	defer tracing.FinishSpan(span)

	var g *errgroup.Group
	g, ctx = errgroup.WithContext(ctx)

	// TODO(dan): This not super principled. I just wanted something that wasn't
	// a constant and grew slower than linear with the length of importSpans. It
	// seems to be working well for BenchmarkRestore2TB but worth revisiting.
	chunkSize := int(math.Sqrt(float64(len(importSpans))))
	importSpanChunks := make([][]importEntry, 0, len(importSpans)/chunkSize)
	for start := 0; start < len(importSpans); {
		importSpanChunk := importSpans[start:]
		end := start + chunkSize
		if end < len(importSpans) {
			importSpanChunk = importSpans[start:end]
		}
		importSpanChunks = append(importSpanChunks, importSpanChunk)
		start = end
	}

	importSpanChunksCh := make(chan []importEntry)
	g.Go(func() error {
		defer close(importSpanChunksCh)
		for idx, importSpanChunk := range importSpanChunks {
			// TODO(dan): The structure between this and the below are very
			// similar. Dedup.
			chunkSpan, err := kr.RewriteSpan(roachpb.Span{
				Key:    importSpanChunk[0].Key,
				EndKey: importSpanChunk[len(importSpanChunk)-1].EndKey,
			})
			if err != nil {
				return err
			}

			// TODO(dan): Really, this should be splitting the Key of the first
			// entry in the _next_ chunk.
			log.VEventf(restoreCtx, 1, "presplitting chunk %d of %d", idx, len(importSpanChunks))
			if err := db.AdminSplit(ctx, chunkSpan.Key, chunkSpan.Key); err != nil {
				return err
			}

			log.VEventf(restoreCtx, 1, "scattering chunk %d of %d", idx, len(importSpanChunks))
			scatterReq := &roachpb.AdminScatterRequest{Span: chunkSpan}
			if _, pErr := client.SendWrapped(ctx, db.GetSender(), scatterReq); pErr != nil {
				// TODO(dan): Unfortunately, Scatter is still too unreliable to
				// fail the RESTORE when Scatter fails. I'm uncomfortable that
				// this could break entirely and not start failing the tests,
				// but on the bright side, it doesn't affect correctness, only
				// throughput.
				log.Errorf(ctx, "failed to scatter chunk %d: %s", idx, pErr.GoError())
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			case importSpanChunksCh <- importSpanChunk:
			}
		}
		return nil
	})

	// TODO(dan): This tries to cover for a bad scatter by having 2 * the number
	// of nodes in the cluster. Is it necessary?
	splitScatterWorkers := numClusterNodes * 2
	var splitScatterStarted uint64 // Only access using atomic.
	for worker := 0; worker < splitScatterWorkers; worker++ {
		g.Go(func() error {
			for importSpanChunk := range importSpanChunksCh {
				for _, importSpan := range importSpanChunk {
					idx := atomic.AddUint64(&splitScatterStarted, 1)

					newSpan, err := kr.RewriteSpan(importSpan.Span)
					if err != nil {
						return err
					}

					// TODO(dan): Really, this should be splitting the Key of
					// the _next_ entry.
					log.VEventf(restoreCtx, 1, "presplitting %d of %d", idx, len(importSpans))
					if err := db.AdminSplit(ctx, newSpan.Key, newSpan.Key); err != nil {
						return err
					}

					log.VEventf(restoreCtx, 1, "scattering %d of %d", idx, len(importSpans))
					scatterReq := &roachpb.AdminScatterRequest{Span: newSpan}
					if _, pErr := client.SendWrapped(ctx, db.GetSender(), scatterReq); pErr != nil {
						// TODO(dan): Unfortunately, Scatter is still too unreliable to
						// fail the RESTORE when Scatter fails. I'm uncomfortable that
						// this could break entirely and not start failing the tests,
						// but on the bright side, it doesn't affect correctness, only
						// throughput.
						log.Errorf(ctx, "failed to scatter %d: %s", idx, pErr.GoError())
					}

					select {
					case <-ctx.Done():
						return ctx.Err()
					case readyForImportCh <- importSpan:
					}
				}
			}
			return nil
		})
	}

	return g.Wait()
}

// Write the new descriptors. First the ID -> TableDescriptor for the new table,
// then flip (or initialize) the name -> ID entry so any new queries will use
// the new one. The tables are assigned the permissions of their parent database
// and the user must have CREATE permission on that database at the time this
// function is called.
func restoreTableDescs(
	ctx context.Context,
	db *client.DB,
	databases []*sqlbase.DatabaseDescriptor,
	tables []*sqlbase.TableDescriptor,
	user string,
) error {
	ctx, span := tracing.ChildSpan(ctx, "restoreTableDescs")
	defer tracing.FinishSpan(span)
	err := db.Txn(ctx, func(ctx context.Context, txn *client.Txn) error {
		b := txn.NewBatch()
		wroteDBs := make(map[sqlbase.ID]*sqlbase.DatabaseDescriptor)
		for _, desc := range databases {
			// TODO(dt): support restoring privs.
			desc.Privileges = sqlbase.NewDefaultPrivilegeDescriptor()
			wroteDBs[desc.ID] = desc
			b.CPut(sqlbase.MakeDescMetadataKey(desc.ID), sqlbase.WrapDescriptor(desc), nil)
			b.CPut(sqlbase.MakeNameMetadataKey(keys.RootNamespaceID, desc.Name), desc.ID, nil)
		}
		for _, table := range tables {
			if wrote, ok := wroteDBs[table.ParentID]; ok {
				table.Privileges = wrote.GetPrivileges()
			} else {
				parentDB, err := sqlbase.GetDatabaseDescFromID(ctx, txn, table.ParentID)
				if err != nil {
					return errors.Wrapf(err, "failed to lookup parent DB %d", table.ParentID)
				}
				if err := sql.CheckPrivilege(user, parentDB, privilege.CREATE); err != nil {
					return err
				}
				// Default is to copy privs from restoring parent db, like CREATE TABLE.
				// TODO(dt): Make this more configurable.
				table.Privileges = parentDB.GetPrivileges()
			}
			b.CPut(table.GetDescMetadataKey(), sqlbase.WrapDescriptor(table), nil)
			b.CPut(table.GetNameMetadataKey(), table.ID, nil)
		}
		if err := txn.Run(ctx, b); err != nil {
			return err
		}

		for _, table := range tables {
			if err := table.Validate(ctx, txn); err != nil {
				return err
			}
		}
		return nil
	})
	return errors.Wrap(err, "restoring table desc and namespace entries")
}

func restoreJobDescription(restore *parser.Restore, from []string) (string, error) {
	r := &parser.Restore{
		AsOf:    restore.AsOf,
		Options: restore.Options,
		Targets: restore.Targets,
		From:    make(parser.Exprs, len(restore.From)),
	}

	for i, f := range from {
		sf, err := storageccl.SanitizeExportStorageURI(f)
		if err != nil {
			return "", err
		}
		r.From[i] = parser.NewDString(sf)
	}

	return parser.AsStringWithFlags(r, parser.FmtSimpleQualified), nil
}

// restore imports a SQL table (or tables) from sets of non-overlapping sstable
// files.
func restore(
	restoreCtx context.Context,
	db *client.DB,
	gossip *gossip.Gossip,
	backupDescs []BackupDescriptor,
	endTime hlc.Timestamp,
	sqlDescs []sqlbase.Descriptor,
	tableRewrites tableRewriteMap,
	job *jobs.Job,
) (roachpb.BulkOpSummary, error) {
	// A note about contexts and spans in this method: the top-level context
	// `restoreCtx` is used for orchestration logging. All operations that carry
	// out work get their individual contexts.

	failed := roachpb.BulkOpSummary{}

	if endTime != (hlc.Timestamp{}) {
		for _, b := range backupDescs {
			if b.StartTime.Less(endTime) && endTime.Less(b.EndTime) && b.MVCCFilter != MVCCFilter_All {
				return failed, errors.Errorf(
					"incompatible RESTORE timestamp (BACKUP needs option '%s')", backupOptRevisionHistory)
			}
		}
	}

	var databases []*sqlbase.DatabaseDescriptor
	var tables []*sqlbase.TableDescriptor
	var oldTableIDs []sqlbase.ID
	for _, desc := range sqlDescs {
		if tableDesc := desc.GetTable(); tableDesc != nil {
			tables = append(tables, tableDesc)
			oldTableIDs = append(oldTableIDs, tableDesc.ID)
		}
		if dbDesc := desc.GetDatabase(); dbDesc != nil {
			if rewrite, ok := tableRewrites[dbDesc.ID]; ok {
				dbDesc.ID = rewrite.TableID
				databases = append(databases, dbDesc)
			}
		}
	}

	log.Eventf(restoreCtx, "starting restore for %d tables", len(tables))

	// We get the spans of the restoring tables _as they appear in the backup_,
	// that is, in the 'old' keyspace, before we reassign the table IDs.
	spans := spansForAllTableIndexes(tables)

	// Assign new IDs and privileges to the tables, and update all references to
	// use the new IDs.
	if err := rewriteTableDescs(tables, tableRewrites); err != nil {
		return failed, err
	}

	// Get TableRekeys to use when importing raw data.
	var rekeys []roachpb.ImportRequest_TableRekey
	for i := range tables {
		newDescBytes, err := protoutil.Marshal(sqlbase.WrapDescriptor(tables[i]))
		if err != nil {
			return failed, errors.Wrap(err, "marshalling descriptor")
		}
		rekeys = append(rekeys, roachpb.ImportRequest_TableRekey{
			OldID:   uint32(oldTableIDs[i]),
			NewDesc: newDescBytes,
		})
	}
	kr, err := storageccl.MakeKeyRewriter(rekeys)
	if err != nil {
		return failed, err
	}

	// Pivot the backups, which are grouped by time, into requests for import,
	// which are grouped by keyrange.
	lowWaterMark := job.Record.Details.(jobs.RestoreDetails).LowWaterMark
	importSpans, _, err := makeImportSpans(spans, backupDescs, lowWaterMark)
	if err != nil {
		return failed, errors.Wrapf(err, "making import requests for %d backups", len(backupDescs))
	}

	var cancel func()
	restoreCtx, cancel = context.WithCancel(restoreCtx)
	defer cancel()
	if err := job.Created(restoreCtx, cancel); err != nil {
		return failed, err
	}
	if err := job.Started(restoreCtx); err != nil {
		return failed, err
	}

	mu := struct {
		syncutil.Mutex
		res               roachpb.BulkOpSummary
		requestsCompleted []bool
		lowWaterMark      int
	}{
		requestsCompleted: make([]bool, len(importSpans)),
		lowWaterMark:      -1,
	}

	progressLogger := jobProgressLogger{
		job:           job,
		totalChunks:   len(importSpans),
		startFraction: job.Payload().FractionCompleted,
		progressedFn: func(progressedCtx context.Context, details interface{}) {
			switch d := details.(type) {
			case *jobs.Payload_Restore:
				mu.Lock()
				if mu.lowWaterMark >= 0 {
					d.Restore.LowWaterMark = importSpans[mu.lowWaterMark].Key
				}
				mu.Unlock()
			default:
				log.Errorf(progressedCtx, "job payload had unexpected type %T", d)
			}
		},
	}

	// We're already limiting these on the server-side, but sending all the
	// Import requests at once would fill up distsender/grpc/something and cause
	// all sorts of badness (node liveness timeouts leading to mass leaseholder
	// transfers, poor performance on SQL workloads, etc) as well as log spam
	// about slow distsender requests. Rate limit them here, too.
	//
	// Use the number of cpus across all nodes in the cluster as the number of
	// outstanding Import requests for the rate limiting. Note that this assumes
	// all nodes in the cluster have the same number of cpus, but it's okay if
	// that's wrong.
	//
	// TODO(dan): Make this limiting per node.
	numClusterNodes := clusterNodeCount(gossip)
	maxConcurrentImports := numClusterNodes * runtime.NumCPU()
	importsSem := make(chan struct{}, maxConcurrentImports)

	g, gCtx := errgroup.WithContext(restoreCtx)

	// The Import (and resulting AddSSTable) requests made below run on
	// leaseholders, so presplit and scatter the ranges to balance the work
	// among many nodes.
	//
	// We're about to start off some goroutines that presplit & scatter each
	// import span. Once split and scattered, the span is submitted to
	// readyForImportCh to indicate it's ready for Import. Since import is so
	// much slower, we buffer the channel to keep the split/scatter work from
	// getting too far ahead. This both naturally rate limits the split/scatters
	// and bounds the number of empty ranges crated if the RESTORE fails (or is
	// cancelled).
	const presplitLeadLimit = 10
	readyForImportCh := make(chan importEntry, presplitLeadLimit)
	g.Go(func() error {
		defer close(readyForImportCh)
		return splitAndScatter(gCtx, db, kr, numClusterNodes, importSpans, readyForImportCh)
	})

	requestFinishedCh := make(chan struct{}, len(importSpans)) // enough buffer to never block
	g.Go(func() error {
		progressCtx, progressSpan := tracing.ChildSpan(gCtx, "progress-log")
		defer tracing.FinishSpan(progressSpan)
		return progressLogger.loop(progressCtx, requestFinishedCh)
	})

	log.Eventf(restoreCtx, "commencing import of data with concurrency %d", maxConcurrentImports)
	tBegin := timeutil.Now()
	var importIdx int
	for readyForImportSpan := range readyForImportCh {
		newSpan, err := kr.RewriteSpan(readyForImportSpan.Span)
		if err != nil {
			return failed, err
		}

		importRequest := &roachpb.ImportRequest{
			// Import is a point request because we don't want DistSender to split
			// it. Assume (but don't require) the entire post-rewrite span is on the
			// same range.
			Span:     roachpb.Span{Key: newSpan.Key},
			DataSpan: readyForImportSpan.Span,
			Files:    readyForImportSpan.files,
			EndTime:  endTime,
			Rekeys:   rekeys,
		}

		importCtx, importSpan := tracing.ChildSpan(gCtx, "import")
		idx := importIdx
		importIdx++
		log.VEventf(restoreCtx, 1, "importing %d of %d", idx, len(importSpans))

		select {
		case importsSem <- struct{}{}:
		case <-gCtx.Done():
			return failed, errors.Wrapf(g.Wait(), "importing %d ranges", len(importSpans))
		}
		log.Event(importCtx, "acquired semaphore")

		g.Go(func() error {
			defer tracing.FinishSpan(importSpan)
			defer func() { <-importsSem }()

			importRes, pErr := client.SendWrapped(importCtx, db.GetSender(), importRequest)
			if pErr != nil {
				return pErr.GoError()
			}

			mu.Lock()
			mu.res.Add(importRes.(*roachpb.ImportResponse).Imported)
			mu.requestsCompleted[idx] = true
			for j := mu.lowWaterMark + 1; j < len(mu.requestsCompleted) && mu.requestsCompleted[j]; j++ {
				mu.lowWaterMark = j
			}
			mu.Unlock()

			requestFinishedCh <- struct{}{}
			return nil
		})
	}

	log.Event(restoreCtx, "wait for outstanding imports to finish")
	if err := g.Wait(); err != nil {
		// This leaves the data that did get imported in case the user wants to
		// retry.
		// TODO(dan): Build tooling to allow a user to restart a failed restore.
		return failed, errors.Wrapf(err, "importing %d ranges", len(importSpans))
	}

	log.Event(restoreCtx, "making tables live")

	// Write the new TableDescriptors and flip the namespace entries over to
	// them. After this call, any queries on a table will be served by the newly
	// restored data.
	if err := restoreTableDescs(restoreCtx, db, databases, tables, job.Record.Username); err != nil {
		return failed, errors.Wrapf(err, "restoring %d TableDescriptors", len(tables))
	}

	// TODO(dan): Delete any old table data here. The first version of restore
	// assumes that it's operating on a new cluster. If it's not empty,
	// everything works but the table data is left abandoned.

	// Don't need the lock any more; we're the only moving part at this stage.
	log.Eventf(restoreCtx, "restore completed: ingested %s of data (before replication) at %s/sec",
		humanizeutil.IBytes(mu.res.DataSize),
		humanizeutil.IBytes(mu.res.DataSize/int64(1+timeutil.Since(tBegin).Seconds())),
	)
	return mu.res, nil
}

var restoreHeader = sqlbase.ResultColumns{
	{Name: "job_id", Typ: parser.TypeInt},
	{Name: "status", Typ: parser.TypeString},
	{Name: "fraction_completed", Typ: parser.TypeFloat},
	{Name: "rows", Typ: parser.TypeInt},
	{Name: "index_entries", Typ: parser.TypeInt},
	{Name: "system_records", Typ: parser.TypeInt},
	{Name: "bytes", Typ: parser.TypeInt},
}

func restorePlanHook(
	stmt parser.Statement, p sql.PlanHookState,
) (func(context.Context, chan<- parser.Datums) error, sqlbase.ResultColumns, error) {
	restoreStmt, ok := stmt.(*parser.Restore)
	if !ok {
		return nil, nil, nil
	}
	if err := utilccl.CheckEnterpriseEnabled(
		p.ExecCfg().Settings, p.ExecCfg().ClusterID(), p.ExecCfg().Organization(), "RESTORE",
	); err != nil {
		return nil, nil, err
	}

	if err := p.RequireSuperUser("RESTORE"); err != nil {
		return nil, nil, err
	}

	fromFn, err := p.TypeAsStringArray(restoreStmt.From, "RESTORE")
	if err != nil {
		return nil, nil, err
	}

	optsFn, err := p.TypeAsStringOpts(restoreStmt.Options, restoreOptionExpectValues)
	if err != nil {
		return nil, nil, err
	}

	fn := func(ctx context.Context, resultsCh chan<- parser.Datums) error {
		// TODO(dan): Move this span into sql.
		ctx, span := tracing.ChildSpan(ctx, stmt.StatementTag())
		defer tracing.FinishSpan(span)

		from, err := fromFn()
		if err != nil {
			return err
		}
		var endTime hlc.Timestamp
		if restoreStmt.AsOf.Expr != nil {
			// Use Now() for the max timestamp because Restore does its own
			// (more restrictive) check.
			var err error
			endTime, err = sql.EvalAsOfTimestamp(nil, restoreStmt.AsOf, p.ExecCfg().Clock.Now())
			if err != nil {
				return err
			}
		}

		opts, err := optsFn()
		if err != nil {
			return err
		}
		return doRestorePlan(ctx, restoreStmt, p, from, endTime, opts, resultsCh)
	}
	return fn, restoreHeader, nil
}

func doRestorePlan(
	ctx context.Context,
	restoreStmt *parser.Restore,
	p sql.PlanHookState,
	from []string,
	endTime hlc.Timestamp,
	opts map[string]string,
	resultsCh chan<- parser.Datums,
) error {
	if err := restoreStmt.Targets.NormalizeTablesWithDatabase(p.EvalContext().Database); err != nil {
		return err
	}
	backupDescs, err := loadBackupDescs(ctx, from, p.ExecCfg().Settings)
	if err != nil {
		return err
	}
	sqlDescs, restoreDBs, err := selectTargets(p, backupDescs, restoreStmt.Targets)
	if err != nil {
		return err
	}
	tableRewrites, err := allocateTableRewrites(ctx, p, sqlDescs, restoreDBs, opts)
	if err != nil {
		return err
	}
	description, err := restoreJobDescription(restoreStmt, from)
	if err != nil {
		return err
	}
	job := p.ExecCfg().JobRegistry.NewJob(jobs.Record{
		Description: description,
		Username:    p.User(),
		DescriptorIDs: func() (sqlDescIDs []sqlbase.ID) {
			for _, tableRewrite := range tableRewrites {
				sqlDescIDs = append(sqlDescIDs, tableRewrite.TableID)
			}
			return sqlDescIDs
		}(),
		Details: jobs.RestoreDetails{
			EndTime:       endTime,
			TableRewrites: tableRewrites,
			URIs:          from,
		},
	})
	res, restoreErr := restore(
		ctx,
		p.ExecCfg().DB,
		p.ExecCfg().Gossip,
		backupDescs,
		endTime,
		sqlDescs,
		tableRewrites,
		job,
	)
	if err := job.FinishedWith(ctx, restoreErr); err != nil {
		return err
	}
	if restoreErr != nil {
		return restoreErr
	}
	// TODO(benesch): emit periodic progress updates.
	resultsCh <- parser.Datums{
		parser.NewDInt(parser.DInt(*job.ID())),
		parser.NewDString(string(jobs.StatusSucceeded)),
		parser.NewDFloat(parser.DFloat(1.0)),
		parser.NewDInt(parser.DInt(res.Rows)),
		parser.NewDInt(parser.DInt(res.IndexEntries)),
		parser.NewDInt(parser.DInt(res.SystemRecords)),
		parser.NewDInt(parser.DInt(res.DataSize)),
	}
	return nil
}

func loadBackupSQLDescs(
	ctx context.Context, details jobs.RestoreDetails, settings *cluster.Settings,
) ([]BackupDescriptor, []sqlbase.Descriptor, error) {
	backupDescs, err := loadBackupDescs(ctx, details.URIs, settings)
	if err != nil {
		return nil, nil, err
	}
	lastBackupDesc := backupDescs[len(backupDescs)-1]

	var sqlDescs []sqlbase.Descriptor
	for _, desc := range lastBackupDesc.Descriptors {
		if _, ok := details.TableRewrites[desc.GetID()]; ok {
			sqlDescs = append(sqlDescs, desc)
		}
	}
	return backupDescs, sqlDescs, nil
}

// restoreFailHook removes KV data that has been committed from a restore that
// has failed or been canceled. It does this by adding the table descriptors
// in DROP state, which causes the schema change stuff to delete the keys
// in the background.
func restoreFailHook(
	ctx context.Context, txn *client.Txn, settings *cluster.Settings, details *jobs.RestoreDetails,
) error {
	// Needed to trigger the schema change manager.
	if err := txn.SetSystemConfigTrigger(); err != nil {
		return err
	}
	_, sqlDescs, err := loadBackupSQLDescs(ctx, *details, settings)
	if err != nil {
		return err
	}
	var tables []*sqlbase.TableDescriptor
	for _, desc := range sqlDescs {
		if tableDesc := desc.GetTable(); tableDesc != nil {
			tableDesc.State = sqlbase.TableDescriptor_DROP
			tables = append(tables, tableDesc)
		}
	}
	if err := rewriteTableDescs(tables, details.TableRewrites); err != nil {
		return err
	}
	b := txn.NewBatch()
	for _, desc := range tables {
		b.CPut(sqlbase.MakeDescMetadataKey(desc.ID), sqlbase.WrapDescriptor(desc), nil)
	}
	return txn.Run(ctx, b)
}

func restoreResumeHook(
	typ jobs.Type, settings *cluster.Settings,
) func(ctx context.Context, job *jobs.Job) error {
	if typ != jobs.TypeRestore {
		return nil
	}

	return func(ctx context.Context, job *jobs.Job) error {
		details := job.Record.Details.(jobs.RestoreDetails)

		backupDescs, sqlDescs, err := loadBackupSQLDescs(ctx, details, settings)
		if err != nil {
			return err
		}

		_, err = restore(
			ctx,
			job.DB(),
			job.Gossip(),
			backupDescs,
			details.EndTime,
			sqlDescs,
			details.TableRewrites,
			job,
		)
		return err
	}
}

func init() {
	sql.AddPlanHook(restorePlanHook)
	jobs.AddResumeHook(restoreResumeHook)
	jobs.RestoreFailHook = restoreFailHook
}
