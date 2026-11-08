package subscription

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeReader struct {
	mu      sync.Mutex
	raw     RawIdentity
	err     error
	calls   int
	release chan struct{} // 非空时 ReadIdentity 阻塞到通道关闭（测并发刷新守卫）
}

func (f *fakeReader) ReadIdentity(ctx context.Context) (RawIdentity, error) {
	f.mu.Lock()
	f.calls++
	rel := f.release
	f.mu.Unlock()
	if rel != nil {
		select {
		case <-rel:
		case <-ctx.Done():
			return RawIdentity{}, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.raw, f.err
}

func (f *fakeReader) set(raw RawIdentity, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.raw = raw
	f.err = err
}

func TestStoreRefreshAutoNumber(t *testing.T) {
	store := NewStore()
	reader := &fakeReader{}
	reader.set(RawIdentity{ICCID: "89860123456789012345", IMEI: "867400000000001", Number: "+8613800138000"}, nil)
	id, err := store.Refresh(context.Background(), reader)
	if err != nil {
		t.Fatalf("Refresh() error=%v", err)
	}
	if id.SubscriptionID != "iccid:89860123456789012345" {
		t.Fatalf("SubscriptionID=%q", id.SubscriptionID)
	}
	if id.Number != "+8613800138000" || id.NumberSource != NumberSourceAuto {
		t.Fatalf("number=%q source=%q", id.Number, id.NumberSource)
	}
	if id.AutoNumber() != "+8613800138000" {
		t.Fatalf("AutoNumber()=%q", id.AutoNumber())
	}
	if id.Generation == 0 {
		t.Fatalf("Generation must be positive after first refresh")
	}
	if _, ok := store.Current(); !ok {
		t.Fatalf("Current() should hit after Refresh")
	}
	if snap, ok := store.Snapshot(); !ok || snap.SubscriptionID != id.SubscriptionID {
		t.Fatalf("Snapshot()=%#v ok=%v", snap, ok)
	}
}

func TestStoreRefreshUnknownWhenNothingReadable(t *testing.T) {
	store := NewStore()
	reader := &fakeReader{}
	reader.set(RawIdentity{}, nil)
	id, err := store.Refresh(context.Background(), reader)
	if err != nil {
		t.Fatalf("Refresh() error=%v", err)
	}
	if id.SubscriptionID != "unknown" || id.Number != "" || id.NumberSource != NumberSourceUnknown {
		t.Fatalf("id=%#v", id)
	}
}

func TestStoreRefreshErrorKeepsNoIdentity(t *testing.T) {
	store := NewStore()
	reader := &fakeReader{}
	reader.set(RawIdentity{}, errors.New("AT timeout"))
	if _, err := store.Refresh(context.Background(), reader); err == nil {
		t.Fatalf("Refresh() should propagate reader error")
	}
	if _, ok := store.Current(); ok {
		t.Fatalf("failed refresh must not create an identity")
	}
}

func TestStoreIdentityChangeBumpsGenerationAndKeepsRemarksSeparate(t *testing.T) {
	store := NewStore()
	reader := &fakeReader{}
	reader.set(RawIdentity{ICCID: "89860123456789012345", Number: "+8613800138000"}, nil)
	first, err := store.Refresh(context.Background(), reader)
	if err != nil {
		t.Fatalf("Refresh() error=%v", err)
	}
	// 给 A 卡备注一个号码。
	if _, err := store.SetRemark("13900000001"); err != nil {
		t.Fatalf("SetRemark() error=%v", err)
	}
	// 换成 B 卡：代次递增、备注不串卡、自动读取生效。
	reader.set(RawIdentity{ICCID: "8944100000000000001", Number: "+447400123456"}, nil)
	second, err := store.Refresh(context.Background(), reader)
	if err != nil {
		t.Fatalf("Refresh() error=%v", err)
	}
	if second.Generation <= first.Generation {
		t.Fatalf("generation did not bump on SIM change: %d -> %d", first.Generation, second.Generation)
	}
	if second.Number != "+447400123456" || second.NumberSource != NumberSourceAuto {
		t.Fatalf("B card must use its own auto number, got %q %q", second.Number, second.NumberSource)
	}
	// 换回 A 卡：备注仍在。
	reader.set(RawIdentity{ICCID: "89860123456789012345", Number: "+8613800138000"}, nil)
	third, err := store.Refresh(context.Background(), reader)
	if err != nil {
		t.Fatalf("Refresh() error=%v", err)
	}
	if third.Number != "13900000001" || third.NumberSource != NumberSourceRemark {
		t.Fatalf("A card remark lost after switch back, got %q %q", third.Number, third.NumberSource)
	}
}

func TestStoreRemarkOverridesAndClearsBackToAuto(t *testing.T) {
	store := NewStore()
	reader := &fakeReader{}
	reader.set(RawIdentity{ICCID: "89860123456789012345", Number: "+8613800138000"}, nil)
	if _, err := store.Refresh(context.Background(), reader); err != nil {
		t.Fatalf("Refresh() error=%v", err)
	}
	remarked, err := store.SetRemark(" 138-0000-1111 ")
	if err != nil {
		t.Fatalf("SetRemark() error=%v", err)
	}
	if remarked.Number != "13800001111" || remarked.NumberSource != NumberSourceRemark {
		t.Fatalf("remarked=%q %q", remarked.Number, remarked.NumberSource)
	}
	if remarked.AutoNumber() != "+8613800138000" {
		t.Fatalf("remark must not overwrite the auto-read number")
	}
	if _, err := store.SetRemark("not-a-number"); err == nil {
		t.Fatalf("SetRemark() should reject non-numeric input")
	}
	cleared, err := store.SetRemark("")
	if err != nil {
		t.Fatalf("SetRemark(clear) error=%v", err)
	}
	if cleared.Number != "+8613800138000" || cleared.NumberSource != NumberSourceAuto {
		t.Fatalf("clearing remark must fall back to auto number, got %q %q", cleared.Number, cleared.NumberSource)
	}
}

func TestStoreSetRemarkWithoutIdentity(t *testing.T) {
	store := NewStore()
	if _, err := store.SetRemark("13800001111"); !errors.Is(err, ErrNoIdentity) {
		t.Fatalf("SetRemark() error=%v want ErrNoIdentity", err)
	}
}

func TestStoreInvalidateClearsCache(t *testing.T) {
	store := NewStore()
	reader := &fakeReader{}
	reader.set(RawIdentity{ICCID: "89860123456789012345", Number: "+8613800138000"}, nil)
	if _, err := store.Refresh(context.Background(), reader); err != nil {
		t.Fatalf("Refresh() error=%v", err)
	}
	store.Invalidate()
	if _, ok := store.Current(); ok {
		t.Fatalf("Invalidate() must clear the cached identity")
	}
	if _, ok := store.Snapshot(); ok {
		t.Fatalf("Snapshot() after invalidate must report unknown attribution")
	}
}

func TestStoreConcurrentRefreshReturnsCache(t *testing.T) {
	store := NewStore()
	reader := &fakeReader{}
	reader.set(RawIdentity{ICCID: "89860123456789012345", Number: "+8613800138000"}, nil)
	// 先让一次刷新缓存住身份，再阻塞下一次读取。
	if _, err := store.Refresh(context.Background(), reader); err != nil {
		t.Fatalf("Refresh() error=%v", err)
	}
	reader.release = make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := store.Refresh(context.Background(), reader)
		done <- err
	}()
	// 等后台刷新确实进入阻塞读取，再并发一次：应直接返回缓存，不增加读取次数。
	deadline := time.Now().Add(2 * time.Second)
	for {
		reader.mu.Lock()
		calls := reader.calls
		reader.mu.Unlock()
		if calls >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background refresh did not start reading in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cached, err := store.Refresh(context.Background(), reader)
	if err != nil {
		t.Fatalf("concurrent Refresh() error=%v", err)
	}
	if cached.Number != "+8613800138000" {
		t.Fatalf("concurrent Refresh() must return the cached identity")
	}
	reader.mu.Lock()
	calls := reader.calls
	reader.mu.Unlock()
	if calls != 2 {
		t.Fatalf("reader calls=%d want 2 (one cached, one in-flight)", calls)
	}
	close(reader.release)
	if err := <-done; err != nil {
		t.Fatalf("blocked Refresh() error=%v", err)
	}
}

func TestStoreSetRemarksLoad(t *testing.T) {
	store := NewStore()
	store.SetRemarks(map[string]string{
		"iccid:89860123456789012345": "13900000001",
		"iccid:bad":                  "nope",
		"":                           "13800001111",
	})
	reader := &fakeReader{}
	reader.set(RawIdentity{ICCID: "89860123456789012345", Number: "+8613800138000"}, nil)
	id, err := store.Refresh(context.Background(), reader)
	if err != nil {
		t.Fatalf("Refresh() error=%v", err)
	}
	if id.Number != "13900000001" || id.NumberSource != NumberSourceRemark {
		t.Fatalf("loaded remark not applied, got %q %q", id.Number, id.NumberSource)
	}
	if got := store.Remarks(); len(got) != 1 || got["iccid:89860123456789012345"] != "13900000001" {
		t.Fatalf("Remarks()=%v", got)
	}
}
