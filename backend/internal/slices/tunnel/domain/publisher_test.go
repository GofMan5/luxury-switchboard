package domain

import "testing"

func TestPublisherProfileBindsPortAndSlug(t *testing.T) {
	profile := "v1.23456.0123456789abcdef0123456789abcdef0123456789abcdef"
	port, slug, err := ParsePublisherProfile(profile)
	if err != nil || port != 23456 || len(slug) != 48 {
		t.Fatalf("valid profile rejected: port=%d slug=%q err=%v", port, slug, err)
	}
	url, _ := PublisherURL(profile)
	if url != PublisherURLPrefix+"/"+slug+"/v1" {
		t.Fatalf("unexpected publisher URL: %s", url)
	}
	if _, _, err := ParsePublisherProfile("v1.12345." + slug); err == nil {
		t.Fatal("out-of-range publisher port accepted")
	}
}
