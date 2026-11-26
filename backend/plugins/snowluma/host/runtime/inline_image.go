package snowlumaruntime

import (
	"context"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/onebotimage"
	snow "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/core"
)

// classifyRetainedImage recognizes the pinned guest's canonical image descriptor
// prefix first. Direct reads own full descriptor and transport validation.
func classifyRetainedImage(scene botruntime.Scene, reference string) botruntime.NativeImageLink {
	file := onebotimage.FilePrefix(scene.ID, reference)
	if payload, inline := onebotimage.InlinePayload(file); inline && len(payload) != 0 && payload[0] != '"' {
		return botruntime.NativeImageInline
	}
	if publicBrowserImageSupported && publicImageLocator(file) {
		return botruntime.NativeImageBrowserDirect
	}
	// Only a non-direct canonical candidate needs the exact token parser.
	// Inline and direct locators can contain large bodies or ignored fields.
	if file == "" || directImageLocator(file) {
		return 0
	}
	media, err := nativeImageDescriptor(scene, reference)
	if err == nil && onebotimage.OpaqueToken(media, str(media, "file")) != "" {
		return botruntime.NativeImageBrowserOpaque
	}
	return 0
}

// readInlineImage accepts only a normalized native guest asset for its canonical
// scene. A URL or platform token is not a complete inline source and is not read.
func readInlineImage(ctx context.Context, scene botruntime.Scene, reference string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	media, err := nativeImageDescriptor(scene, reference)
	if err != nil {
		return nil, err
	}
	return decodeInlineImage(str(media, "file"))
}

func nativeImageDescriptor(scene botruntime.Scene, reference string) (*jsjson.Object, error) {
	if scene.ID != snow.SceneID(scene.AdapterID, scene.AccountID, scene.Kind, scene.PlatformSceneID) {
		return nil, botruntime.ErrDisplayImageUnavailable
	}
	media, err := onebotimage.Descriptor(scene.ID, reference)
	if err != nil {
		return nil, botruntime.ErrDisplayImageUnavailable
	}
	return media, nil
}

func decodeInlineImage(file string) ([]byte, error) {
	value, err := onebotimage.DecodeInline(file)
	if err == onebotimage.ErrUnavailable {
		return nil, botruntime.ErrDisplayImageUnavailable
	}
	return value, err
}
