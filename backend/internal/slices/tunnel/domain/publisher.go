package domain

import (
	"errors"
	"regexp"
	"strconv"
)

const PublisherURLPrefix = "https://luxuryprivate.duckdns.org/model-tunnel"

var publisherProfilePattern = regexp.MustCompile(`^v1\.([0-9]{5})\.([0-9a-f]{48})$`)

func ParsePublisherProfile(value string) (int, string, error) {
	match := publisherProfilePattern.FindStringSubmatch(value)
	if len(match) != 3 {
		return 0, "", errors.New("invalid tunnel publisher profile")
	}
	port, _ := strconv.Atoi(match[1])
	if port < 20000 || port > 29999 {
		return 0, "", errors.New("invalid tunnel publisher profile")
	}
	return port, match[2], nil
}

func PublisherURL(value string) (string, error) {
	_, slug, err := ParsePublisherProfile(value)
	if err != nil {
		return "", err
	}
	return PublisherURLPrefix + "/" + slug + "/v1", nil
}
