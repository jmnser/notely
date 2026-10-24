package main

import (
	"errors"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/maxence-charriere/go-app/v10/pkg/app"

	"github.com/jmnser/notely/internal/note"
)

// imageAccept and maxImage are the image types and size the backend stores.
const (
	imageAccept = "image/png,image/jpeg,image/gif,image/webp"
	maxImage    = 2 << 20
)

// checkImage explains why file (a JavaScript File) cannot be uploaded, if so.
func checkImage(f app.Value) string {
	if !slices.Contains(strings.Split(imageAccept, ","), f.Get("type").String()) {
		return "Use a PNG, JPEG, GIF or WebP image."
	}
	if f.Get("size").Int() > maxImage {
		return "Images can be up to 2 MiB."
	}
	return ""
}

// imageFiles returns the files of a JavaScript FileList, or explains why
// one cannot be uploaded.
func imageFiles(files app.Value) ([]app.Value, string) {
	var list []app.Value
	for i := range files.Length() {
		f := files.Index(i)
		if msg := checkImage(f); msg != "" {
			return nil, msg
		}
		list = append(list, f)
	}
	return list, ""
}

// imageMarkdown is a Markdown image of src, described by the file name.
func imageMarkdown(file, src string) string {
	alt := strings.TrimSuffix(file, path.Ext(file))
	alt = strings.NewReplacer("[", "", "]", "", "\n", " ", "\\", "").Replace(alt)
	return "![" + alt + "](" + src + ")\n"
}

// versions maps the paths of the images to their versions.
func (u *ui) versions() map[string]string {
	m := make(map[string]string, len(u.images))
	for _, img := range u.images {
		m[img.Path()] = img.Version
	}
	return m
}

// renderImage renders a row for image img in its space.
func (u *ui) renderImage(img note.Image) app.UI {
	row := "note-row image-row"
	if img.Path() == u.image {
		row += " current"
	}
	scope := app.EventScope("image:" + img.Path())
	return app.Li().Class(row).Body(
		app.Button().Class("note").Body(
			app.Span().Class("note-title").Body(icon("image"), app.Span().Text(img.Name)),
			app.Span().Class("note-meta").Text(img.Uploaded.Local().Format("Jan 2, 15:04")),
		).OnClick(func(app.Context, app.Event) { u.openImage(img.Path()) }, scope),
		iconButton("edit", "Rename image").OnClick(func(app.Context, app.Event) { u.renameImage(img) }, scope),
		iconButton("delete", "Delete").OnClick(func(app.Context, app.Event) { u.removeImage(img) }, scope),
	)
}

// renderImageView renders the open image in the main pane.
func (u *ui) renderImageView() app.UI {
	img := u.findImage(u.image)
	if img == nil {
		img = &note.Image{}
	}
	users, ref := 0, note.Image{Space: img.Space, Name: img.Name}
	for _, n := range u.notes {
		if n.Kind != note.Drawing && slices.Contains(note.ImageRefs(n.Body, n.Space), ref) {
			users++
		}
	}
	used := "Not used by any note."
	switch {
	case users == 1:
		used = "Used by 1 note."
	case users > 1:
		used = "Used by " + strconv.Itoa(users) + " notes."
	}
	return app.Div().Class("image-view").Body(
		app.Header().Class("top-bar").Body(
			app.Div().Class("crumbs").Body(
				iconButton("arrow_back", "Back").Class("icon-button back").
					OnClick(func(app.Context, app.Event) { u.openImage("") }),
				app.Span().Class("path").Body(icon("folder"), app.Span().Text(
					strings.ReplaceAll(note.SpaceName(img.Space), "/", " / "))),
			),
			app.Div().Class("tools").Body(
				iconButton("edit", "Rename image").OnClick(func(app.Context, app.Event) { u.renameImage(*img) }),
				iconButton("move", "Move to space").OnClick(func(app.Context, app.Event) { u.moveImage(*img) }),
				iconButton("delete", "Delete").OnClick(func(app.Context, app.Event) { u.removeImage(*img) }),
			),
		),
		app.H1().Class("image-name").Text(img.Name),
		app.P().Class("image-ref").Body(
			app.Code().Text("![]("+note.ImageRef(img.Space, img.Space, img.Name)+")"),
			app.Span().Text(" in its space, "),
			app.Code().Text("![]("+note.ImageRef("\x00", img.Space, img.Name)+")"), // from no space: by path
			app.Span().Text(" elsewhere. "+used),
		).Hidden(img.Name == ""),
		app.Div().Class("image-image").Body(
			app.If(img.Name != "", func() app.UI {
				return app.Img().Src(imageURL(rootPrefix(), *img, img.Version)).Alt(img.Name)
			}),
		),
		app.Input().ID("image-upload").Type("file").Accept(imageAccept).Multiple(true).Hidden(true).
			OnChange(func(ctx app.Context, _ app.Event) {
				u.uploadImages(ctx.JSSrc().Get("files"), u.uploadTo)
				ctx.JSSrc().Set("value", "") // the same file can be picked again
			}),
	)
}

func (u *ui) findImage(p string) *note.Image {
	for i := range u.images {
		if u.images[i].Path() == p {
			return &u.images[i]
		}
	}
	return nil
}

// openImage shows image p, a path, in the main pane, or closes it for "".
func (u *ui) openImage(p string) {
	u.image = p // first, so that open keeps the history entry
	if p != "" {
		u.open("")
	}
	u.syncBack()
}

// loadImages fetches the images, closes the open one if it is gone and then
// calls then, if not nil.
func (u *ui) loadImages(then func()) {
	gen := u.syncGen
	u.ctx.Async(func() {
		list, err := listImages()
		u.ctx.Dispatch(func(app.Context) {
			if gen != u.syncGen { // signed out meanwhile
				return
			}
			if err != nil {
				app.Log(err)
				return
			}
			if !slices.Equal(list, u.images) {
				u.images = list
				u.imagesGen++
			}
			if u.image != "" && u.findImage(u.image) == nil {
				u.openImage("")
			}
			if then != nil {
				then()
			}
		})
	})
}

// pickImages opens the file picker to upload images to space.
func (u *ui) pickImages(space string) {
	u.uploadTo = space
	app.Window().GetElementByID("image-upload").Call("click")
}

// uploadImages uploads image files (a JavaScript FileList) to space.
func (u *ui) uploadImages(files app.Value, space string) {
	list, msg := imageFiles(files)
	if msg != "" || len(list) == 0 {
		u.status = msg
		u.ctx.Update()
		return
	}
	u.status = "Uploading image…"
	gen := u.syncGen
	u.ctx.Async(func() {
		var last note.Image
		var err error
		for _, f := range list {
			if last, err = addImage(f, space); err != nil {
				break
			}
		}
		u.ctx.Dispatch(func(app.Context) {
			u.imageDone(gen, err, "Could not upload the image: ", func() {
				u.expand(last.Space)
				u.openImage(last.Path())
			})
		})
	})
}

// imageDone reports the outcome of an image request started in sign-on gen,
// reloads the images and then, on success, calls then if not nil.
func (u *ui) imageDone(gen int, err error, failed string, then func()) {
	switch {
	case gen != u.syncGen: // signed out meanwhile
		return
	case errors.Is(err, errSignedOut):
		u.signedOut, u.status = true, ""
		u.listen()
		return
	case err != nil:
		u.status = failed + err.Error()
		then = nil
	default:
		u.status = ""
	}
	u.loadImages(then)
}

// repoint points the references to image from in the notes with pending
// changes to image to, as the backend did in its notes; these would replace
// them.
func (u *ui) repoint(from, to note.Image) {
	for id := range u.dirty {
		if n := u.find(id); n != nil && n.Kind != note.Drawing {
			if body, ok := note.RewriteImageRefs(n.Body, n.Space, from, to); ok {
				u.editNote(id, func(n *note.Note) { n.Body = body })
				if id == u.current {
					app.Window().GetElementByID("body").Set("value", body)
				}
			}
		}
	}
}

// changeImage renames or moves image img and keeps it open under its new
// name. The backend points the notes' references to it.
func (u *ui) changeImage(img note.Image, name, space string) {
	open, gen := u.image == img.Path(), u.syncGen
	u.ctx.Async(func() {
		moved, err := moveImage(img, name, space)
		u.ctx.Dispatch(func(ctx app.Context) {
			if err == nil {
				u.repoint(img, moved)
				if open {
					u.image = moved.Path() // before the reload, which closes a gone image
				}
			}
			u.imageDone(gen, err, "Not changed: ", func() {
				u.expand(moved.Space)
				u.sync(ctx) // the notes pointing to it
			})
		})
	})
}

// moveImages moves the images of space s and its subspaces along when it is
// renamed to name. Images moved meanwhile, along with their notes, are left.
func (u *ui) moveImages(s, name string) {
	var list []note.Image
	for _, img := range u.images {
		if note.Within(img.Space, s) {
			list = append(list, img)
		}
	}
	if len(list) == 0 {
		return
	}
	gen := u.syncGen
	u.ctx.Async(func() {
		var err error
		var moved []note.Image
		for _, img := range list {
			to, e := moveImage(img, img.Stem(), note.SpacePath(name+img.Space[len(s):]))
			if errors.Is(e, errNotFound) {
				continue
			} else if e != nil {
				err = e
				break
			}
			moved = append(moved, img, to)
		}
		u.ctx.Dispatch(func(ctx app.Context) {
			for i := 0; i+1 < len(moved); i += 2 {
				u.repoint(moved[i], moved[i+1])
			}
			u.imageDone(gen, err, "Some images were not moved: ", func() { u.sync(ctx) })
		})
	})
}

// renameImage asks for a new name of image img, a DNS label.
func (u *ui) renameImage(img note.Image) {
	if img.Name == "" {
		return
	}
	u.ask(prompt{
		title: "Rename image", ok: "Save", input: true,
		text: "Up to 63 lower case letters, digits and -, starting and ending with a letter or digit. " +
			"Notes using the image are changed along.",
		run: func(name string) {
			if name != img.Stem() {
				u.changeImage(img, name, img.Space)
			}
		},
	}, img.Stem())
}

// moveImage asks for the space to move image img to.
func (u *ui) moveImage(img note.Image) {
	if img.Name == "" {
		return
	}
	u.ask(prompt{
		title: "Move to space", ok: "Move", input: true, list: true,
		text: "Pick a space or type a new path, e.g. Work/Projects. Notes using the image are changed along.",
		run: func(name string) {
			if space := note.SpacePath(name); space != img.Space {
				u.changeImage(img, img.Stem(), space)
			}
		},
	}, note.SpaceName(img.Space))
}

// removeImage asks to delete image img.
func (u *ui) removeImage(img note.Image) {
	if img.Name == "" {
		return
	}
	u.ask(prompt{
		title: "Delete image?", ok: "Delete", danger: true,
		text: "“" + img.Name + "” will be deleted from " + note.SpaceName(img.Space) +
			". Notes using it show a missing image.",
		run: func(string) {
			if u.image == img.Path() {
				u.openImage("")
			}
			gen := u.syncGen
			u.ctx.Async(func() {
				err := deleteImage(img)
				u.ctx.Dispatch(func(app.Context) { u.imageDone(gen, err, "Not deleted: ", nil) })
			})
		},
	}, "")
}

// insertImages uploads image files (a JavaScript FileList) next to the open
// note and inserts a Markdown image for each at the editor's cursor, in order.
func (u *ui) insertImages(files app.Value) {
	list, msg := imageFiles(files)
	if msg != "" || len(list) == 0 {
		u.status = msg
		u.ctx.Update()
		return
	}
	cur := u.find(u.current)
	if cur == nil {
		return
	}
	id, n := cur.ID, *cur
	u.status = "Uploading image…"
	u.ctx.Async(func() {
		var text strings.Builder
		var err error
		for _, f := range list {
			var name string
			if name, err = uploadImage(n, f); err != nil {
				break
			}
			text.WriteString(imageMarkdown(f.Get("name").String(), name))
		}
		u.ctx.Dispatch(func(app.Context) {
			switch {
			case errors.Is(err, errSignedOut):
				u.signedOut, u.status = true, ""
				u.listen()
			case err != nil:
				u.status = "Could not upload the image: " + err.Error()
			default:
				u.status = ""
			}
			if text.Len() > 0 {
				u.loadImages(nil)
			}
			if text.Len() > 0 && u.current == id && u.editing {
				u.insertText(text.String())
			}
		})
	})
}

// openGallery shows the images in the open note's space, to insert one.
func (u *ui) openGallery(app.Context, app.Event) {
	u.imagesMsg = "Loading…"
	app.Window().GetElementByID("images").Call("showModal")
	u.loadImages(func() { u.imagesMsg = "" })
}

// renderGallery renders the dialog listing the images in the open note's space.
func (u *ui) renderGallery() app.UI {
	space := ""
	if cur := u.find(u.current); cur != nil {
		space = cur.Space
	}
	var list []note.Image
	for _, img := range u.images {
		if img.Space == space {
			list = append(list, img)
		}
	}
	msg := u.imagesMsg
	if msg == "" && len(list) == 0 {
		msg = "No images in this space yet."
	}
	return app.Dialog().ID("images").Body(
		app.H2().Text("Images in this space"),
		app.P().Text("Select one to insert it.").Hidden(len(list) == 0),
		app.P().Attr("role", "status").Text(msg).Hidden(msg == ""),
		app.Div().Class("gallery").Body(app.Range(list).Slice(func(i int) app.UI {
			img := list[i]
			return app.Button().Type("button").Class("gallery-item").Title("Insert "+img.Name).
				OnClick(func(app.Context, app.Event) {
					app.Window().GetElementByID("images").Call("close")
					if u.editing {
						u.insertText(imageMarkdown("", img.Name))
					}
				}).
				Body(
					app.Img().Src(imageURL(rootPrefix(), img, img.Version)).Alt("").Attr("loading", "lazy"),
					app.Code().Text(img.Name),
				)
		})),
		app.Div().Class("actions").Body(
			app.Span().Class("spacer"),
			app.Button().Type("button").Class("text-button").Text("Close").OnClick(func(app.Context, app.Event) {
				app.Window().GetElementByID("images").Call("close")
			}),
		),
	)
}
