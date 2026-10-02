package models

import (
	"context"

	"github.com/stashapp/stash/pkg/txn"
)

type TxnManager interface {
	txn.Manager
	txn.DatabaseProvider
}

type Repository struct {
	TxnManager TxnManager

	Blob           BlobReader
	File           FileReaderWriter
	Folder         FolderReaderWriter
	Gallery        GalleryReaderWriter
	GalleryChapter GalleryChapterReaderWriter
	Image          ImageReaderWriter
	Group          GroupReaderWriter
	// stash#837: without this field the issues store is unreachable from any consumer
	// that resolves its dependencies from the Repository rather than constructing a
	// store directly -- the API layer, dlna, the scan task. A store that exists and is
	// wired into sqlite.Database but not here is a store nothing can use.
	Issue       IssueReaderWriter
	Performer   PerformerReaderWriter
	Scene       SceneReaderWriter
	SceneMarker SceneMarkerReaderWriter
	Studio      StudioReaderWriter
	Tag         TagReaderWriter
	SavedFilter SavedFilterReaderWriter
}

func (r *Repository) WithTxn(ctx context.Context, fn txn.TxnFunc) error {
	return txn.WithTxn(ctx, r.TxnManager, fn)
}

func (r *Repository) WithReadTxn(ctx context.Context, fn txn.TxnFunc) error {
	return txn.WithReadTxn(ctx, r.TxnManager, fn)
}

func (r *Repository) WithDB(ctx context.Context, fn txn.TxnFunc) error {
	return txn.WithDatabase(ctx, r.TxnManager, fn)
}
