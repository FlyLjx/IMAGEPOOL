package adobe

import (
	"errors"
	"strings"
	"testing"
)

func TestImageJobManagerTracksResultAndDeduplicatesAccount(t *testing.T) {
	manager := newTestImageJobManager()
	job, created, err := manager.Start("account-1", DefaultModelID, "16:9")
	if err != nil || !created || job.Status != "running" {
		t.Fatalf("job=%#v created=%v err=%v", job, created, err)
	}
	duplicate, created, err := manager.Start("account-1", DefaultModelID, "16:9")
	if err != nil || created || duplicate.ID != job.ID {
		t.Fatalf("duplicate=%#v created=%v err=%v", duplicate, created, err)
	}
	manager.Update(job.ID, "starting_generation", "向 Adobe 提交生图任务", 25, nil)
	manager.Succeed(job.ID, ImageGenerateResult{Images: [][]byte{{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}}, UpstreamJobID: "upstream-1"})
	completed, ok := manager.Get(job.ID)
	if !ok || completed.Status != "succeeded" || completed.Percent != 100 || completed.UpstreamJobID != "upstream-1" || !strings.HasPrefix(completed.ImageDataURL, "data:image/png;base64,") {
		t.Fatalf("completed=%#v ok=%v", completed, ok)
	}

	next, created, err := manager.Start("account-1", DefaultModelID, "16:9")
	if err != nil || !created || next.ID == job.ID {
		t.Fatalf("next=%#v created=%v err=%v", next, created, err)
	}
	manager.Fail(next.ID, errors.New("upstream failed"))
	failed, ok := manager.Get(next.ID)
	if !ok || failed.Status != "failed" || failed.Error != "upstream failed" {
		t.Fatalf("failed=%#v ok=%v", failed, ok)
	}
}
