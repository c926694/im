package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type response struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data any    `json:"data"`
}

const (
	codeSuccess             = 0
	codeInvalidRequest      = 40001
	codeInvalidUsername     = 40002
	codeInvalidToken        = 40003
	codeInvalidCredentials  = 40004
	codeUsernameTaken       = 40005
	codeFriendshipRequired  = 40006
	codeNotConversationUser = 40007
	codeInvalidMessage      = 40008
	codeInvalidPagination   = 40009
	codeResourceNotFound    = 40010
	codeResourceConflict    = 40011
	codeInsufficientPoints  = 40012
	codeRedPacketClaimed    = 40013
	codeRedPacketExpired    = 40014
	codeInternalError       = 50000
)

type responseDefinition struct {
	code int
	msg  string
}

var responseDefinitions = map[string]responseDefinition{
	"invalid_request":             {code: codeInvalidRequest, msg: "请求参数错误"},
	"invalid_registration":        {code: codeInvalidRequest, msg: "注册参数错误"},
	"invalid_login":               {code: codeInvalidRequest, msg: "登录参数错误"},
	"invalid_peer_username":       {code: codeInvalidUsername, msg: "用户名格式错误"},
	"invalid_friend_decision":     {code: codeInvalidRequest, msg: "好友申请处理参数错误"},
	"invalid_friend_request_id":   {code: codeInvalidRequest, msg: "好友申请标识错误"},
	"invalid_friend_id":           {code: codeInvalidRequest, msg: "好友标识错误"},
	"invalid_conversation_id":     {code: codeInvalidRequest, msg: "会话标识错误"},
	"invalid_message":             {code: codeInvalidMessage, msg: "消息参数错误"},
	"invalid_client_msg_id":       {code: codeInvalidMessage, msg: "客户端消息标识错误"},
	"invalid_content":             {code: codeInvalidMessage, msg: "消息内容错误"},
	"invalid_pagination":          {code: codeInvalidPagination, msg: "分页参数错误"},
	"invalid_read_seq":            {code: codeInvalidRequest, msg: "已读序号错误"},
	"invalid_token":               {code: codeInvalidToken, msg: "访问凭证无效"},
	"invalid_credentials":         {code: codeInvalidCredentials, msg: "用户名或密码错误"},
	"invalid_ws_ticket":           {code: codeInvalidToken, msg: "WebSocket 票据无效"},
	"username_taken":              {code: codeUsernameTaken, msg: "用户名已经存在"},
	"self_conversation":           {code: codeInvalidRequest, msg: "不能和自己建立单聊"},
	"self_friend_request":         {code: codeInvalidRequest, msg: "不能向自己发送好友申请"},
	"not_friends":                 {code: codeFriendshipRequired, msg: "成为好友后才能发送消息"},
	"not_conversation_member":     {code: codeNotConversationUser, msg: "没有权限访问这个会话"},
	"user_not_found":              {code: codeResourceNotFound, msg: "用户不存在"},
	"friend_request_unavailable":  {code: codeResourceNotFound, msg: "好友申请不存在或已经处理"},
	"route_not_found":             {code: codeResourceNotFound, msg: "请求路径不存在"},
	"incoming_friend_request":     {code: codeResourceConflict, msg: "对方已经向你发送好友申请"},
	"already_friends":             {code: codeResourceConflict, msg: "你们已经是好友"},
	"internal_error":              {code: codeInternalError, msg: "服务器内部错误"},
	"invalid_group":               {code: codeInvalidRequest, msg: "群聊参数错误"},
	"group_unavailable":           {code: codeResourceNotFound, msg: "群聊不存在或已经关闭"},
	"group_permission":            {code: codeNotConversationUser, msg: "当前群角色无权进行此操作"},
	"group_member_missing":        {code: codeResourceNotFound, msg: "该用户不在群内"},
	"group_limit":                 {code: codeResourceConflict, msg: "群成员不能超过 200 人"},
	"group_invite_friends":        {code: codeFriendshipRequired, msg: "只能邀请自己的好友加入群聊"},
	"owner_must_transfer":         {code: codeResourceConflict, msg: "请先转让群主身份，再退出群聊"},
	"message_conflict":            {code: codeResourceConflict, msg: "客户端消息标识已用于其他请求"},
	"invalid_red_packet":          {code: codeInvalidRequest, msg: "红包参数错误"},
	"invalid_red_packet_shares":   {code: codeInvalidRequest, msg: "红包份数超出可领取人数"},
	"red_packet_not_found":        {code: codeResourceNotFound, msg: "红包不存在"},
	"red_packet_ineligible":       {code: codeNotConversationUser, msg: "当前账号无法领取此红包"},
	"red_packet_claimed":          {code: codeRedPacketClaimed, msg: "红包已领完"},
	"red_packet_expired":          {code: codeRedPacketExpired, msg: "红包已过期"},
	"insufficient_points":         {code: codeInsufficientPoints, msg: "积分余额不足"},
	"previous_membership_message": {code: codeResourceConflict, msg: "该发送标识属于上一次入群"},
}

func respond(c *gin.Context, code int, msg string, data any) {
	c.JSON(http.StatusOK, response{Code: code, Msg: msg, Data: data})
}

func respondSuccess(c *gin.Context, data any) {
	respond(c, codeSuccess, "success", data)
}

func respondError(c *gin.Context, code int, msg string) {
	c.AbortWithStatusJSON(http.StatusOK, response{Code: code, Msg: msg, Data: nil})
}

func reject(c *gin.Context, key string) {
	definition, ok := responseDefinitions[key]
	if !ok {
		definition = responseDefinitions["internal_error"]
	}
	respondError(c, definition.code, definition.msg)
}
