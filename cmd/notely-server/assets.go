package main

import (
	"errors"
	"slices"
	"strings"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// assetsKey prefixes the Assets space's entries in ui.collapsed; note space
// paths never start with it.
const assetsKey = "\x00assets"

// assetSpace is the Assets space or one of its subspaces.
type assetSpace struct {
	name   string // "" for the Assets space
	assets []asset
}

// assetSpaces groups the assets matching query: the Assets space, then its
// subspaces by name.
func assetSpaces(assets []asset, query string) (top assetSpace, subs []assetSpace) {
	bySpace := map[string][]asset{}
	for _, a := range assets {
		if strings.Contains(a.key(), query) {
			bySpace[a.Space] = append(bySpace[a.Space], a)
		}
	}
	top = assetSpace{assets: bySpace[""]}
	delete(bySpace, "")
	for name, list := range bySpace {
		subs = append(subs, assetSpace{name: name, assets: list})
	}
	slices.SortFunc(subs, func(a, b assetSpace) int { return strings.Compare(a.name, b.name) })
	return top, subs
}

// renderAssets renders the Assets space, below the other spaces: the user's
// uploaded images, in one level of subspaces.
func (u *ui) renderAssets(query string) app.UI {
	top, subs := assetSpaces(u.assets, query)
	count := len(top.assets)
	for _, s := range subs {
		count += len(s.assets)
	}
	return u.renderAssetSpace(top, count, query != "", subs)
}

func (u *ui) renderAssetSpace(s assetSpace, count int, searching bool, subs []assetSpace) app.UI {
	key, label, upload := assetsKey, "Assets", "Upload images"
	if s.name != "" {
		key, label, upload = assetsKey+"/"+s.name, s.name, "Upload images to "+s.name
	}
	open := searching || !u.collapsed[key]
	class := "space assets"
	if !open {
		class += " collapsed"
	}
	return app.Li().Body(
		app.Div().Class(class).Body(
			app.Button().Class("space-name").Aria("expanded", open).Body(
				app.Span().Class("chevron").Body(icon("expand_more")),
				app.Span().Class("label").Text(label),
				app.Span().Class("count").Text(count),
			).OnClick(func(app.Context, app.Event) {
				u.collapsed[key] = !u.collapsed[key]
			}, app.EventScope(key)),
			iconButton("add", upload).OnClick(func(app.Context, app.Event) {
				u.uploadTo = s.name
				app.Window().GetElementByID("asset-file").Call("click")
			}, app.EventScope(key)),
		),
		app.If(open, func() app.UI {
			return app.Ul().Body(
				app.Range(subs).Slice(func(i int) app.UI {
					return u.renderAssetSpace(subs[i], len(subs[i].assets), searching, nil)
				}),
				app.Range(s.assets).Slice(func(i int) app.UI {
					return u.renderAsset(s.assets[i])
				}),
			)
		}),
	)
}

// renderAsset renders a row with asset a's thumbnail.
func (u *ui) renderAsset(a asset) app.UI {
	row := "note-row asset-row"
	if a.key() == u.asset {
		row += " current"
	}
	scope := app.EventScope(a.key())
	return app.Li().Class(row).Body(
		app.Button().Class("note").Body(
			app.Img().Class("thumb").Src(assetURL(a)).Alt("").Attr("loading", "lazy"),
			app.Span().Class("note-title").Text(a.Name),
			app.Span().Class("note-meta").Text(a.Uploaded.Local().Format("Jan 2, 15:04")),
		).OnClick(func(app.Context, app.Event) { u.openAsset(a.key()) }, scope),
		iconButton("edit", "Rename image").OnClick(func(app.Context, app.Event) { u.renameAsset(a) }, scope),
		iconButton("delete", "Delete").OnClick(func(app.Context, app.Event) { u.removeAsset(a) }, scope),
	)
}

// renderAssetView renders the open asset in the main pane.
func (u *ui) renderAssetView() app.UI {
	a := u.findAsset(u.asset)
	if a == nil {
		a = &asset{}
	}
	path := "Assets"
	if a.Space != "" {
		path += " / " + a.Space
	}
	return app.Div().Class("asset-view").Body(
		app.Header().Class("top-bar").Body(
			app.Div().Class("crumbs").Body(
				iconButton("arrow_back", "Back").Class("icon-button back").
					OnClick(func(app.Context, app.Event) { u.openAsset("") }),
				app.Span().Class("path").Body(icon("photo_library"), app.Span().Text(path)),
			),
			app.Div().Class("tools").Body(
				iconButton("edit", "Rename image").OnClick(func(app.Context, app.Event) { u.renameAsset(*a) }),
				iconButton("move", "Move to subspace").OnClick(func(app.Context, app.Event) { u.moveAsset(*a) }),
				iconButton("delete", "Delete").OnClick(func(app.Context, app.Event) { u.removeAsset(*a) }),
			),
		),
		app.H1().Class("asset-name").Text(a.Name),
		app.Div().Class("asset-image").Body(
			app.If(a.Name != "", func() app.UI { return app.Img().Src(assetURL(*a)).Alt(a.Name) }),
		),
		app.Input().ID("asset-file").Type("file").Accept(imageAccept).Multiple(true).Hidden(true).
			OnChange(func(ctx app.Context, _ app.Event) {
				u.uploadAssets(ctx.JSSrc().Get("files"), u.uploadTo)
				ctx.JSSrc().Set("value", "") // the same file can be picked again
			}),
	)
}

func (u *ui) findAsset(key string) *asset {
	for i := range u.assets {
		if u.assets[i].key() == key {
			return &u.assets[i]
		}
	}
	return nil
}

// openAsset shows asset key in the main pane, or closes it for "".
func (u *ui) openAsset(key string) {
	if key != "" {
		u.open("")
	}
	u.asset = key
	u.syncBack()
}

// loadAssets fetches the user's assets.
func (u *ui) loadAssets() {
	u.ctx.Async(func() {
		list, err := listAssets()
		u.ctx.Dispatch(func(app.Context) {
			if err != nil {
				app.Log(err)
				return
			}
			u.assets = list
		})
	})
}

// uploadAssets uploads image files (a JavaScript FileList) to subspace space
// of the Assets space.
func (u *ui) uploadAssets(files app.Value, space string) {
	var list []app.Value
	for i := range files.Length() {
		f := files.Index(i)
		if msg := checkImage(f); msg != "" {
			u.status = msg
			u.ctx.Update()
			return
		}
		list = append(list, f)
	}
	if len(list) == 0 {
		return
	}
	u.status = "Uploading image…"
	u.ctx.Async(func() {
		var last asset
		var err error
		for _, f := range list {
			if last, err = uploadAsset(f, space); err != nil {
				break
			}
		}
		u.ctx.Dispatch(func(app.Context) {
			u.assetDone(err, "Could not upload the image: ")
			if err == nil {
				u.expandAssets(space)
				u.openAsset(last.key())
			}
		})
	})
}

// expandAssets opens the Assets space and its subspace space.
func (u *ui) expandAssets(space string) {
	delete(u.collapsed, assetsKey)
	delete(u.collapsed, assetsKey+"/"+space)
}

// assetDone reports the outcome of an asset request and reloads the assets.
func (u *ui) assetDone(err error, failed string) {
	switch {
	case errors.Is(err, errSignedOut):
		u.signedOut, u.status = true, ""
		u.listen()
		return
	case err != nil:
		u.status = failed + err.Error()
	default:
		u.status = ""
	}
	u.loadAssets()
}

// changeAsset renames or moves asset a and opens it under its new name.
func (u *ui) changeAsset(a asset, name, space string) {
	open := u.asset == a.key()
	u.ctx.Async(func() {
		moved, err := putAsset(a, name, space)
		u.ctx.Dispatch(func(app.Context) {
			u.assetDone(err, "Not changed: ")
			if err == nil && open {
				u.expandAssets(moved.Space)
				u.asset = moved.key()
			}
		})
	})
}

// renameAsset asks for a new name of asset a, a DNS label.
func (u *ui) renameAsset(a asset) {
	if a.Name == "" {
		return
	}
	u.ask(prompt{
		title: "Rename image", ok: "Save", input: true,
		text: "Up to 63 lower case letters, digits and -, starting and ending with a letter or digit.",
		run: func(name string) {
			if name != a.stem() {
				u.changeAsset(a, name, a.Space)
			}
		},
	}, a.stem())
}

// moveAsset asks for the subspace to move asset a to.
func (u *ui) moveAsset(a asset) {
	if a.Name == "" {
		return
	}
	u.ask(prompt{
		title: "Move to subspace", ok: "Move", input: true,
		text: "Type a subspace name, named like images, or leave empty for Assets. Assets has one level of subspaces.",
		run: func(space string) {
			if space != a.Space {
				u.changeAsset(a, a.stem(), space)
			}
		},
	}, a.Space)
}

// removeAsset asks to delete asset a. Notes keep their own copies of images.
func (u *ui) removeAsset(a asset) {
	if a.Name == "" {
		return
	}
	u.ask(prompt{
		title: "Delete image?", ok: "Delete", danger: true,
		text: "“" + a.Name + "” will be deleted from Assets. Notes using it keep their copy.",
		run: func(string) {
			if u.asset == a.key() {
				u.openAsset("")
			}
			u.ctx.Async(func() {
				err := call(del, "/api/assets/"+a.key(), nil, nil)
				u.ctx.Dispatch(func(app.Context) { u.assetDone(err, "Not deleted: ") })
			})
		},
	}, "")
}
