package main

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	"google.golang.org/api/youtube/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	grpcoauth "google.golang.org/grpc/credentials/oauth"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

var youtubeVideoIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

type youtubePage struct {
	messages      []youtubeMessage
	nextPageToken string
	offline       bool
}

type youtubeMessage struct {
	id          string
	author      string
	text        string
	messageType string
	publishedAt string
}

type youtubeAPI interface {
	activeLiveChatID(context.Context, oauth2.TokenSource, string) (string, error)
	stream(context.Context, oauth2.TokenSource, string, string, func(youtubePage) error) error
}

type googleYouTubeAPI struct{}

type youtubeStreamDescriptors struct {
	request  protoreflect.MessageDescriptor
	response protoreflect.MessageDescriptor
	message  protoreflect.MessageDescriptor
	snippet  protoreflect.MessageDescriptor
	author   protoreflect.MessageDescriptor
}

var (
	youtubeDescriptorOnce  sync.Once
	youtubeDescriptorValue youtubeStreamDescriptors
	youtubeDescriptorErr   error
)

func youtubeGRPCDescriptors() (youtubeStreamDescriptors, error) {
	youtubeDescriptorOnce.Do(func() {
		optional := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
		repeated := descriptorpb.FieldDescriptorProto_LABEL_REPEATED
		stringType := descriptorpb.FieldDescriptorProto_TYPE_STRING
		int32Type := descriptorpb.FieldDescriptorProto_TYPE_INT32
		messageType := descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
		field := func(name string, number int32, label descriptorpb.FieldDescriptorProto_Label, kind descriptorpb.FieldDescriptorProto_Type, typeName string) *descriptorpb.FieldDescriptorProto {
			value := &descriptorpb.FieldDescriptorProto{
				Name:   proto.String(name),
				Number: proto.Int32(number),
				Label:  &label,
				Type:   &kind,
			}
			if typeName != "" {
				value.TypeName = proto.String(typeName)
			}
			return value
		}
		file, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
			Syntax:  proto.String("proto2"),
			Name:    proto.String("stream_list_minimal.proto"),
			Package: proto.String("youtube.api.v3"),
			MessageType: []*descriptorpb.DescriptorProto{
				{
					Name: proto.String("LiveChatMessageListRequest"),
					Field: []*descriptorpb.FieldDescriptorProto{
						field("live_chat_id", 1, optional, stringType, ""),
						field("page_token", 99, optional, stringType, ""),
						field("part", 100, repeated, stringType, ""),
					},
				},
				{
					Name: proto.String("LiveChatMessageListResponse"),
					Field: []*descriptorpb.FieldDescriptorProto{
						field("offline_at", 2, optional, stringType, ""),
						field("next_page_token", 100602, optional, stringType, ""),
						field("items", 1007, repeated, messageType, ".youtube.api.v3.LiveChatMessage"),
					},
				},
				{
					Name: proto.String("LiveChatMessage"),
					Field: []*descriptorpb.FieldDescriptorProto{
						field("id", 101, optional, stringType, ""),
						field("snippet", 2, optional, messageType, ".youtube.api.v3.LiveChatMessageSnippet"),
						field("author_details", 3, optional, messageType, ".youtube.api.v3.LiveChatMessageAuthorDetails"),
					},
				},
				{
					Name: proto.String("LiveChatMessageSnippet"),
					Field: []*descriptorpb.FieldDescriptorProto{
						field("type", 1, optional, int32Type, ""),
						field("published_at", 4, optional, stringType, ""),
						field("display_message", 16, optional, stringType, ""),
					},
				},
				{
					Name: proto.String("LiveChatMessageAuthorDetails"),
					Field: []*descriptorpb.FieldDescriptorProto{
						field("display_name", 103, optional, stringType, ""),
					},
				},
			},
			Service: []*descriptorpb.ServiceDescriptorProto{{
				Name: proto.String("V3DataLiveChatMessageService"),
				Method: []*descriptorpb.MethodDescriptorProto{{
					Name:            proto.String("StreamList"),
					InputType:       proto.String(".youtube.api.v3.LiveChatMessageListRequest"),
					OutputType:      proto.String(".youtube.api.v3.LiveChatMessageListResponse"),
					ServerStreaming: proto.Bool(true),
				}},
			}},
		}, nil)
		if err != nil {
			youtubeDescriptorErr = err
			return
		}
		messages := file.Messages()
		youtubeDescriptorValue = youtubeStreamDescriptors{
			request:  messages.ByName("LiveChatMessageListRequest"),
			response: messages.ByName("LiveChatMessageListResponse"),
			message:  messages.ByName("LiveChatMessage"),
			snippet:  messages.ByName("LiveChatMessageSnippet"),
			author:   messages.ByName("LiveChatMessageAuthorDetails"),
		}
	})
	return youtubeDescriptorValue, youtubeDescriptorErr
}

func setDynamicString(message *dynamicpb.Message, name protoreflect.Name, value string) {
	if value == "" {
		return
	}
	field := message.Descriptor().Fields().ByName(name)
	message.Set(field, protoreflect.ValueOfString(value))
}

func dynamicString(message protoreflect.Message, name protoreflect.Name) string {
	field := message.Descriptor().Fields().ByName(name)
	if field == nil || !message.Has(field) {
		return ""
	}
	return message.Get(field).String()
}

func youtubePageFromGRPC(response *dynamicpb.Message, descriptors youtubeStreamDescriptors) youtubePage {
	page := youtubePage{
		nextPageToken: dynamicString(response, "next_page_token"),
		offline:       dynamicString(response, "offline_at") != "",
	}
	itemsField := descriptors.response.Fields().ByName("items")
	items := response.Get(itemsField).List()
	page.messages = make([]youtubeMessage, 0, items.Len())
	for index := 0; index < items.Len(); index++ {
		item := items.Get(index).Message()
		id := dynamicString(item, "id")
		if id == "" {
			continue
		}
		var author, text, publishedAt, messageType string
		if snippetField := descriptors.message.Fields().ByName("snippet"); item.Has(snippetField) {
			snippet := item.Get(snippetField).Message()
			text = dynamicString(snippet, "display_message")
			publishedAt = dynamicString(snippet, "published_at")
			typeField := descriptors.snippet.Fields().ByName("type")
			if snippet.Has(typeField) {
				messageType = youtubeMessageTypeName(int32(snippet.Get(typeField).Int()))
			}
		}
		if authorField := descriptors.message.Fields().ByName("author_details"); item.Has(authorField) {
			author = dynamicString(item.Get(authorField).Message(), "display_name")
		}
		page.messages = append(page.messages, youtubeMessage{
			id:          id,
			author:      author,
			text:        text,
			messageType: messageType,
			publishedAt: publishedAt,
		})
	}
	return page
}

func youtubeMessageTypeName(value int32) string {
	return map[int32]string{
		0:  "invalidType",
		1:  "textMessageEvent",
		2:  "tombstone",
		3:  "fanFundingEvent",
		4:  "chatEndedEvent",
		5:  "sponsorOnlyModeStartedEvent",
		6:  "sponsorOnlyModeEndedEvent",
		7:  "newSponsorEvent",
		8:  "messageDeletedEvent",
		9:  "messageRetractedEvent",
		10: "userBannedEvent",
		15: "superChatEvent",
		16: "superStickerEvent",
		17: "memberMilestoneChatEvent",
		18: "membershipGiftingEvent",
		19: "giftMembershipReceivedEvent",
		20: "pollEvent",
		21: "giftEvent",
	}[value]
}

func parseYouTubeVideoID(rawURL string) (string, error) {
	if len(rawURL) > 2048 {
		return "", errors.New("YouTube配信URLが長すぎます")
	}
	parsed, err := url.ParseRequestURI(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme != "https" || parsed.User != nil {
		return "", errors.New("httpsのYouTube配信URLを入力してください")
	}
	if parsed.Port() != "" && parsed.Port() != "443" {
		return "", errors.New("YouTube配信URLのポート番号が正しくありません")
	}

	host := strings.ToLower(parsed.Hostname())
	var videoID string
	switch host {
	case "youtube.com", "www.youtube.com", "m.youtube.com":
		segments := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
		switch {
		case parsed.Path == "/watch":
			videoID = parsed.Query().Get("v")
		case len(segments) == 2 && (segments[0] == "live" || segments[0] == "shorts"):
			value, unescapeErr := url.PathUnescape(segments[1])
			if unescapeErr == nil {
				videoID = value
			}
		}
	case "youtu.be":
		segments := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
		if len(segments) == 1 {
			value, unescapeErr := url.PathUnescape(segments[0])
			if unescapeErr == nil {
				videoID = value
			}
		}
	default:
		return "", errors.New("YouTubeまたはyoutu.beの配信URLを入力してください")
	}
	if !youtubeVideoIDPattern.MatchString(videoID) {
		return "", errors.New("YouTube配信URLから動画IDを取得できませんでした")
	}
	return videoID, nil
}

func (googleYouTubeAPI) activeLiveChatID(ctx context.Context, tokenSource oauth2.TokenSource, videoID string) (string, error) {
	service, err := youtube.NewService(ctx, option.WithTokenSource(tokenSource))
	if err != nil {
		return "", err
	}
	response, err := service.Videos.List([]string{"liveStreamingDetails"}).Id(videoID).Context(ctx).Do()
	if err != nil {
		return "", err
	}
	if len(response.Items) == 0 {
		return "", errors.New("youtube video not found")
	}
	details := response.Items[0].LiveStreamingDetails
	if details == nil || details.ActiveLiveChatId == "" {
		return "", errors.New("youtube active live chat not found")
	}
	return details.ActiveLiveChatId, nil
}

func (googleYouTubeAPI) stream(ctx context.Context, tokenSource oauth2.TokenSource, liveChatID, pageToken string, receive func(youtubePage) error) error {
	descriptors, err := youtubeGRPCDescriptors()
	if err != nil {
		return err
	}
	connection, err := grpc.NewClient(
		"dns:///youtube.googleapis.com:443",
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: "youtube.googleapis.com",
		})),
		grpc.WithPerRPCCredentials(grpcoauth.TokenSource{TokenSource: tokenSource}),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(8*1024*1024)),
	)
	if err != nil {
		return err
	}
	defer connection.Close()

	request := dynamicpb.NewMessage(descriptors.request)
	setDynamicString(request, "live_chat_id", liveChatID)
	setDynamicString(request, "page_token", pageToken)
	parts := request.Mutable(descriptors.request.Fields().ByName("part")).List()
	for _, part := range []string{"id", "snippet", "authorDetails"} {
		parts.Append(protoreflect.ValueOfString(part))
	}
	stream, err := connection.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, "/youtube.api.v3.V3DataLiveChatMessageService/StreamList")
	if err != nil {
		return err
	}
	if err := stream.SendMsg(request); err != nil {
		return err
	}
	if err := stream.CloseSend(); err != nil {
		return err
	}
	for {
		response := dynamicpb.NewMessage(descriptors.response)
		if err := stream.RecvMsg(response); err != nil {
			return err
		}
		if err := receive(youtubePageFromGRPC(response, descriptors)); err != nil {
			return err
		}
	}
}

func friendlyYouTubeError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "YouTubeとの通信がタイムアウトしました。もう一度お試しください。"
	}
	if err.Error() == "youtube video not found" {
		return "指定したYouTube動画が見つかりませんでした。URLを確認してください。"
	}
	if err.Error() == "youtube active live chat not found" {
		return "この動画には受信可能なライブチャットがありません。配信中か確認してください。"
	}
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		for _, detail := range apiErr.Errors {
			switch detail.Reason {
			case "liveChatEnded":
				return "YouTube Liveは終了しています。"
			case "liveChatDisabled":
				return "この配信ではライブチャットが無効です。"
			case "quotaExceeded", "dailyLimitExceeded":
				return "YouTube APIの利用上限に達しました。時間をおいてお試しください。"
			case "forbidden", "insufficientPermissions":
				return "ライブチャットを読む権限がありません。Google認証をやり直してください。"
			}
		}
	}
	switch status.Code(err) {
	case codes.FailedPrecondition:
		return "YouTube Liveが終了しているか、ライブチャットが無効です。"
	case codes.PermissionDenied, codes.Unauthenticated:
		return "ライブチャットを読む権限がありません。Google認証をやり直してください。"
	case codes.NotFound:
		return "YouTube Liveのチャットが見つかりませんでした。"
	case codes.ResourceExhausted:
		return "YouTube APIの利用上限に達しました。時間をおいてお試しください。"
	case codes.InvalidArgument:
		return "YouTube Liveの接続情報が無効です。配信URLを確認してください。"
	}
	return "YouTubeとの接続に失敗しました。通信状態を確認してもう一度お試しください。"
}

func retryableYouTubeError(err error) (bool, time.Duration) {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false, 0
	}
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		for _, detail := range apiErr.Errors {
			if detail.Reason == "rateLimitExceeded" {
				return true, parseRetryAfter(apiErr.Header)
			}
		}
		switch apiErr.Code {
		case http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true, parseRetryAfter(apiErr.Header)
		default:
			return false, 0
		}
	}
	switch status.Code(err) {
	case codes.ResourceExhausted, codes.Unavailable, codes.Aborted, codes.Internal, codes.DeadlineExceeded:
		return true, 0
	case codes.PermissionDenied, codes.Unauthenticated, codes.NotFound, codes.InvalidArgument, codes.FailedPrecondition:
		return false, 0
	}
	return true, 0
}

func parseRetryAfter(header http.Header) time.Duration {
	value := header.Get("Retry-After")
	if value == "" {
		return 0
	}
	if parsed, err := time.Parse(time.RFC1123, value); err == nil {
		if delay := time.Until(parsed); delay > 0 {
			return delay
		}
	}
	seconds, err := strconv.Atoi(value)
	if err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return 0
}
