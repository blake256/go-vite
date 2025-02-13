package consensus

import (
	"time"

	"github.com/vitelabs/go-vite/v2/common"
	"github.com/vitelabs/go-vite/v2/common/types"
)

type subscriber_puppet struct {
	*consensusSubscriber

	snapshot DposReader
}

func newSubscriberPuppet(sub interface{}, snapshot DposReader) *subscriber_puppet {
	switch v := sub.(type) {
	case *consensusSubscriber:
		return &subscriber_puppet{
			consensusSubscriber: v,
			snapshot:            snapshot,
		}
	}
	panic("err sub type")
}

func (cs subscriber_puppet) triggerEvent(gid types.Gid, fn func(*subscribeEvent)) {
	if gid == types.SNAPSHOT_GID {
		return
	}
	cs.consensusSubscriber.triggerEvent(gid, fn)
}

func (cs subscriber_puppet) TriggerMineEvent(addr types.Address) error {
	sTime := time.Unix(time.Now().Unix(), 0)
	eTime := sTime.Add(time.Duration(cs.snapshot.GetInfo().Interval))
	index := cs.snapshot.Time2Index(sTime)
	periodStartTime, periodEndTime := cs.snapshot.Index2Time(index)
	voteTime := cs.snapshot.GenProofTime(index)

	cs.consensusSubscriber.triggerEvent(types.SNAPSHOT_GID, func(e *subscribeEvent) {
		common.Go(func() {
			event := Event{
				Gid:         types.SNAPSHOT_GID,
				Address:     addr,
				Stime:       sTime,
				Etime:       eTime,
				Timestamp:   sTime,
				VoteTime:    voteTime,
				PeriodStime: periodStartTime,
				PeriodEtime: periodEndTime,
			}
			e.fn(event)
		})

	})
	return nil
}
