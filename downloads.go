package msfp

import (
	"context"
	"slices"

	"github.com/msfp-io/msfp-go/wire"
)

// Downloads kept for their late reports (MSFP Media playing 6): a
// download's progress and stop with at count for 30 days after its play,
// on both sides, across restarts. A store that implements DownloadsTx
// keeps them; the engine keeps them in memory besides, read back at
// Open. A peer's download here that was live at a restart is ended then:
// its ticket worked in memory only, its late reports still count.

// loadDownloads reads back the downloads the store keeps, those past
// their 30 days removed.
func (e *Engine) loadDownloads(ctx context.Context) error {
	var kept, past []DownloadRecord
	err := e.cfg.Store.Read(ctx, func(tx Tx) error {
		d, ok := tx.(DownloadsTx)
		if !ok {
			return nil
		}
		all, err := d.Downloads()
		kept = all
		return err
	})
	if err != nil {
		return err
	}
	now := e.now()
	ps, rs := &e.plays, &e.remotes
	ps.mu.Lock()
	rs.mu.Lock()
	for _, d := range kept {
		switch {
		case now.Sub(d.PlayedAt) > wire.LateReports:
			past = append(past, d)
		case d.Viewer:
			if rs.downloads == nil {
				rs.downloads = map[string]pastDownload{}
			}
			rs.downloads[d.ID] = pastDownload{peer: d.Peer, theirID: d.TheirID, title: d.Title, queue: d.Queue, playedAt: d.PlayedAt}
		default:
			if ps.ended == nil {
				ps.ended = map[string]endedPlay{}
			}
			ps.ended[d.ID] = endedPlay{peer: d.Peer, reason: EndStopped, at: d.PlayedAt, purpose: wire.PurposeDownload,
				title: d.Title, queue: d.Queue, handle: d.Handle, playedAt: d.PlayedAt, lastAt: d.LastAt}
		}
	}
	rs.mu.Unlock()
	ps.mu.Unlock()
	e.dropDownloads(ctx, past)
	return nil
}

// keepDownload adds or replaces a download in the store, when it keeps
// them; a failure is logged: the download is still kept in memory.
func (e *Engine) keepDownload(ctx context.Context, d DownloadRecord) {
	d.Queue = slices.Clone(d.Queue)
	err := e.cfg.Store.Update(ctx, func(tx Tx) error {
		if k, ok := tx.(DownloadsTx); ok {
			return k.PutDownload(d)
		}
		return nil
	})
	if err != nil {
		e.log.Warn("msfp: a download couldn't be kept for its late reports", "error", err)
	}
}

// dropDownloads removes downloads past their 30 days from the store.
func (e *Engine) dropDownloads(ctx context.Context, past []DownloadRecord) {
	if len(past) == 0 {
		return
	}
	err := e.cfg.Store.Update(ctx, func(tx Tx) error {
		k, ok := tx.(DownloadsTx)
		if !ok {
			return nil
		}
		for _, d := range past {
			if err := k.DeleteDownload(d.Viewer, d.ID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		e.log.Warn("msfp: downloads past their late reports couldn't be removed", "error", err)
	}
}
