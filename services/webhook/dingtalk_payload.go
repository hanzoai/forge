// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package webhook

// The DingTalk message shapes.
//
// These were the only two things used from gitea.com/lunny/dingtalk_webhook —
// pure JSON structs. The rest of that package is an HTTP client that posts them,
// which this forge never called: every webhook goes out through the shared
// deliverer, with its own retries, proxy settings and rate limits. So the
// dependency existed to supply four struct definitions.
//
// Field names and JSON tags are DingTalk's, not ours:
// https://developers.dingtalk.com/document/app/custom-robot-access

// DingtalkLinkMsg is one entry in a feedCard.
type DingtalkLinkMsg struct {
	Title      string `json:"title"`
	MessageURL string `json:"messageURL"`
	PicURL     string `json:"picURL"`
}

// DingtalkActionCard is a message with buttons.
type DingtalkActionCard struct {
	Text           string `json:"text"`
	Title          string `json:"title"`
	HideAvatar     string `json:"hideAvatar"`
	BtnOrientation string `json:"btnOrientation"`
	SingleTitle    string `json:"singleTitle"`
	SingleURL      string `json:"singleURL"`
	Buttons        []struct {
		Title     string `json:"title"`
		ActionURL string `json:"actionURL"`
	} `json:"btns"`
}

// DingtalkPayloadMsg is the request body DingTalk accepts. Which member is read
// depends on MsgType, so the unused ones marshal as empty objects — that is the
// API's shape, not an oversight.
type DingtalkPayloadMsg struct {
	MsgType string `json:"msgtype"`
	Text    struct {
		Content string `json:"content"`
	} `json:"text"`
	Link struct {
		Text       string `json:"text"`
		Title      string `json:"title"`
		PicURL     string `json:"picUrl"`
		MessageURL string `json:"messageUrl"`
	} `json:"link"`
	Markdown struct {
		Text  string `json:"text"`
		Title string `json:"title"`
	} `json:"markdown"`
	ActionCard DingtalkActionCard `json:"actionCard"`
	FeedCard   struct {
		Links []DingtalkLinkMsg `json:"links"`
	} `json:"feedCard"`
	At struct {
		AtMobiles []string `json:"atMobiles"`
		IsAtAll   bool     `json:"isAtAll"`
	} `json:"at"`
}
