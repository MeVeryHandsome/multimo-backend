package qa

import (
	"context"

	"github.com/gogf/gf/v2/errors/gcode"
	"github.com/gogf/gf/v2/errors/gerror"

	v1 "multimo-backend/api/qa/v1"
	svc "multimo-backend/internal/service/qa"
)

type ControllerV1 struct {
	Svc *svc.Service
}

func (c *ControllerV1) Ask(ctx context.Context, req *v1.AskReq) (*v1.AskRes, error) {
	res, err := c.Svc.Answer(ctx, svc.Request{Question: req.Question, Collections: req.Collections})
	if err != nil {
		return nil, gerror.WrapCode(gcode.CodeInternalError, err, "问答失败")
	}
	return &v1.AskRes{Response: res}, nil
}
