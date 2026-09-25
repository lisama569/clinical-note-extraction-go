// Command note-demo 使用本地构造的病历文本与术语资源，演示分节切分、
// 概念抽取、否定/不确定性判定、缩写消歧与术语版本切换。
// 不访问任何外部服务、不下载模型、不含真实病历数据。
//
// 用法：
//
//	go run ./cmd/note-demo            # 默认 v2
//	go run ./cmd/note-demo -version v1
package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/lisama569/clinical-note-extraction-go/pkg/pipeline"
)

const demoNote = "门诊接诊记录（本行在任何小节标题之前，用于演示缺标题保留）\n" +
	"现病史：\n" +
	"患者3天前\n" + // 跨行内容，与下行合并为同一句
	"出现发热、咳嗽，无胸痛；否认呼吸困难。\n" +
	"神经科长期随访 MS，考虑脱髓鞘病变。\n" +
	"心脏超声提示 MS，建议瓣膜评估。\n" +
	"诊断：\n" +
	"MS" // 末行无换行、无结束符：演示截断检测

func main() {
	version := flag.String("version", "", "术语版本：v1 或 v2（留空使用最新版本）")
	flag.Parse()

	p := pipeline.New(nil)
	ctx := context.Background()
	if *version != "" {
		ctx = pipeline.WithVersion(ctx, *version)
	}
	out, err := p.Process(ctx, demoNote)
	if err != nil {
		fmt.Println("处理被拒绝：", err)
		return
	}
	fmt.Print(out.String())
}
