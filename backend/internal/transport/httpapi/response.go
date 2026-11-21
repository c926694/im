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
	codeInternalError       = 50000
)

type responseDefinition struct {
	code int
	msg  string
}

var responseDefinitions = map[string]responseDefinition{
	"invalid_request":            {code: codeInvalidRequest, msg: "请求参数错误"},
	"invalid_registration":       {code: codeInvalidRequest, msg: "注册参数错误"},
	"invalid_login":              {code: codeInvalidRequest, msg: "登录参数错误"},
	"invalid_peer_username":      {code: codeInvalidUsername, msg: "用户名格式错误"},
	"invalid_friend_decision":    {code: codeInvalidRequest, msg: "好友申请处理参数错误"},
	"invalid_friend_request_id":  {code: codeInvalidRequest, msg: "好友申请标识错误"},
	"invalid_friend_id":          {code: codeInvalidRequest, msg: "好友标识错误"},
	"invalid_conversation_id":    {code: codeInvalidRequest, msg: "会话标识错误"},
	"invalid_message":            {code: codeInvalidMessage, msg: "消息参数错误"},
	"invalid_client_msg_id":      {code: codeInvalidMessage, msg: "客户端消息标识错误"},
	"invalid_content":            {code: codeInvalidMessage, msg: "消息内容错误"},
	"invalid_pagination":         {code: codeInvalidPagination, msg: "分页参数错误"},
	"invalid_read_seq":           {code: codeInvalidRequest, msg: "已读序号错误"},
	"invalid_token":              {code: codeInvalidToken, msg: "访问凭证无效"},
	"invalid_credentials":        {code: codeInvalidCredentials, msg: "用户名或密码错误"},
	"invalid_ws_ticket":          {code: codeInvalidToken, msg: "WebSocket 票据无效"},
	"username_taken":             {code: codeUsernameTaken, msg: "用户名已经存在"},
	"self_conversation":          {code: codeInvalidRequest, msg: "不能和自己建立单聊"},
	"self_friend_request":        {code: codeInvalidRequest, msg: "不能向自己发送好友申请"},
	"not_friends":                {code: codeFriendshipRequired, msg: "成为好友后才能发送消息"},
	"not_conversation_member":    {code: codeNotConversationUser, msg: "没有权限访问这个会话"},
	"user_not_found":             {code: codeResourceNotFound, msg: "用户不存在"},
	"friend_request_unavailable": {code: codeResourceNotFound, msg: "好友申请不存在或已经处理"},
	"route_not_found":            {code: codeResourceNotFound, msg: "请求路径不存在"},
	"incoming_friend_request":    {code: codeResourceConflict, msg: "对方已经向你发送好友申请"},
	"already_friends":            {code: codeResourceConflict, msg: "你们已经是好友"},
	"internal_error":             {code: codeInternalError, msg: "服务器内部错误"},
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
