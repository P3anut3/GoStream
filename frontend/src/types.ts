export interface User { id: number; username: string }
export interface LoginResponse extends User { token: string }
export interface Video {
  id: number
  author_id: number
  title: string
  play_url: string
  cover_url: string
  created_at: string
  author_name?: string
  like_count?: number
  liked?: boolean
}
export interface FeedResponse { videos: Video[]; next_cursor: string; has_more: boolean }
export interface VideoPage { videos: Video[]; page: number; page_size: number }
export interface LikeStatus { like_count: number; liked: boolean }
export interface FollowStatus { following: boolean }
export interface Comment { id: number; user_id: number; video_id: number; content: string; created_at: string }
export interface CommentPage { comments: Comment[]; page: number; page_size: number; video_id: number }
export interface VideoUpload { play_url: string; saved_name: string; filename: string; size: number }
export interface CoverUpload { cover_url: string; saved_name: string; filename: string; size: number }
