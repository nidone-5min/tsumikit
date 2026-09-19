export const OVERLAY_EVENT_SCHEMA_VERSION = '1' as const;

export const OVERLAY_EVENT_PLATFORMS = ['youtube', 'twitch', 'test'] as const;
export type OverlayEventPlatform = (typeof OVERLAY_EVENT_PLATFORMS)[number];

export const OVERLAY_EVENT_TYPES = [
  'comment',
  'paid_message',
  'paid_sticker',
  'bits',
  'membership',
  'membership_gift',
  'raid',
  'follow',
  'channel_points',
  'comment_deleted',
  'user_comments_cleared',
  'chat_cleared',
  'stream_started',
  'stream_ended',
] as const;
export type OverlayEventType = (typeof OVERLAY_EVENT_TYPES)[number];

export interface StreamInfo {
  id: string;
  channelId?: string;
  channelName?: string;
}

export interface AuthorBadge {
  id: string;
  name: string;
  imageUrl?: string;
}

export interface AuthorInfo {
  id: string;
  displayName: string;
  login?: string;
  imageUrl?: string;
  badges: AuthorBadge[];
  isBroadcaster: boolean;
  isModerator: boolean;
  isMemberOrSubscriber: boolean;
  color?: string;
}

export type MessageFragment =
  | { type: 'text' | 'emoji'; text: string; emote?: never }
  | { type: 'emote'; text: string; emote: EmoteInfo };

export interface EmoteInfo {
  id: string;
  displayName: string;
  imageUrl: string;
}

export interface MessageInfo {
  text: string;
  fragments: MessageFragment[];
  publishedAt: string;
  deleted: boolean;
}

export interface OverlayEvent<
  TPayload = unknown,
  TPlatformExtra extends Record<string, unknown> = Record<string, unknown>,
> {
  schemaVersion: typeof OVERLAY_EVENT_SCHEMA_VERSION;
  id: string;
  platform: OverlayEventPlatform;
  type: OverlayEventType;
  occurredAt: string;
  receivedAt: string;
  replayed: boolean;
  test: boolean;
  stream: StreamInfo;
  author?: AuthorInfo;
  message?: MessageInfo;
  payload: TPayload;
  platformExtra?: TPlatformExtra;
}
