package images // import "github.com/docker/docker/daemon/images"

import (
	"encoding/json"
	"io"

	"github.com/docker/docker/api/types/backend"
	"github.com/docker/docker/builder"
	"github.com/docker/docker/image"
	"github.com/docker/docker/layer"
	"github.com/docker/docker/pkg/ioutils"
	"github.com/docker/docker/pkg/system"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

// CommitImage creates a new image from a commit config
func (i *ImageService) CommitImage(c backend.CommitConfig) (image.ID, error) {
	layerStore, ok := i.layerStores[c.ContainerOS]
	if !ok {
		return "", system.ErrNotSupportedOperatingSystem
	}
	rwTar, err := exportContainerRw(layerStore, c.ContainerID, c.ContainerMountLabel)
	if err != nil {
		return "", err
	}
	defer func() {
		if rwTar != nil {
			rwTar.Close()
		}
	}()

	var parent *image.Image
	if c.ParentImageID == "" {
		parent = new(image.Image)
		parent.RootFS = image.NewRootFS()
	} else {
		parent, err = i.imageStore.Get(image.ID(c.ParentImageID))
		if err != nil {
			return "", err
		}
	}

	l, err := layerStore.Register(rwTar, parent.RootFS.ChainID())
	if err != nil {
		return "", err
	}
	defer layer.ReleaseAndLog(layerStore, l)

	cc := image.ChildConfig{
		ContainerID:     c.ContainerID,
		Author:          c.Author,
		Comment:         c.Comment,
		ContainerConfig: c.ContainerConfig,
		Config:          c.Config,
		DiffID:          l.DiffID(),
	}
	config, err := json.Marshal(image.NewChildImage(parent, cc, c.ContainerOS))
	if err != nil {
		return "", err
	}

	id, err := i.imageStore.Create(config)
	if err != nil {
		return "", err
	}

	if c.ParentImageID != "" {
		if err := i.imageStore.SetParent(id, image.ID(c.ParentImageID)); err != nil {
			return "", err
		}
	}
	return id, nil
}

func exportContainerRw(layerStore layer.Store, id, mountLabel string) (arch io.ReadCloser, err error) {
	rwlayer, err := layerStore.GetRWLayer(id)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			layerStore.ReleaseRWLayer(rwlayer)
		}
	}()

	// TODO: this mount call is not necessary as we assume that TarStream() should
	// mount the layer if needed. But the Diff() function for windows requests that
	// the layer should be mounted when calling it. So we reserve this mount call
	// until windows driver can implement Diff() interface correctly.
	_, err = rwlayer.Mount(mountLabel)
	if err != nil {
		return nil, err
	}

	archive, err := rwlayer.TarStream()
	if err != nil {
		rwlayer.Unmount()
		return nil, err
	}
	return ioutils.NewReadCloserWrapper(archive, func() error {
			archive.Close()
			err = rwlayer.Unmount()
			layerStore.ReleaseRWLayer(rwlayer)
			return err
		}),
		nil
}

// CommitBuildStep is used by the builder to create an image for each step in
// the build.
//
// This method is different from CreateImageFromContainer:
//   - it doesn't attempt to validate container state
//   - it doesn't send a commit action to metrics
//   - it doesn't log a container commit event
//
// This is a temporary shim. Should be removed when builder stops using commit.
func (i *ImageService) CommitBuildStep(c backend.CommitConfig, confused bool) (image.ID, error) {
	container := i.containers.Get(c.ContainerID)
	if container == nil {
		// TODO: use typed error
		return "", errors.Errorf("container not found: %s", c.ContainerID)
	}
	c.ContainerMountLabel = container.MountLabel
	c.ContainerOS = container.OS
	if c.ParentImageID == "" {
		c.ParentImageID = string(container.ImageID)
	}

	if confused {
		return i.CommitConfusedImage(c)
	}
	return i.CommitImage(c)
}

func (i *ImageService) CommitConfusedImage(c backend.CommitConfig) (image.ID, error) {
	layerStore, ok := i.layerStores[c.ContainerOS]
	if !ok {
		return "", system.ErrNotSupportedOperatingSystem
	}

	var parent *image.Image
	var err error

	if c.ParentImageID == "" {
		parent = new(image.Image)
		parent.RootFS = image.NewRootFS()
	} else {
		parent, err = i.imageStore.Get(image.ID(c.ParentImageID))
		if err != nil {
			return "", err
		}
	}
	mountID, err := layerStore.GetMountID(c.ContainerID)
	if err != nil {
		return "", err
	}
	l, err := layerStore.ConfusedLayer(parent.RootFS.ChainID(), mountID)
	if err != nil {
		return "", err
	}
	defer layer.ReleaseAndLog(layerStore, l)

	cc := image.ChildConfig{
		ContainerID:     c.ContainerID,
		Author:          c.Author,
		Comment:         c.Comment,
		ContainerConfig: c.ContainerConfig,
		Config:          c.Config,
		DiffID:          l.DiffID(),
	}
	config, err := json.Marshal(image.NewChildImage(parent, cc, c.ContainerOS))
	if err != nil {
		return "", err
	}

	id, err := i.imageStore.Create(config)
	if err != nil {
		return "", err
	}

	if c.ParentImageID != "" {
		if err := i.imageStore.SetParent(id, image.ID(c.ParentImageID)); err != nil {
			return "", err
		}
	}
	return id, nil
}

func (i *ImageService) CommitAddOrCopy(c backend.CommitConfig) (*image.Image, builder.ROLayer, error) {
	layerStore, ok := i.layerStores[c.ContainerOS]
	if !ok {
		return nil, nil, system.ErrNotSupportedOperatingSystem
	}

	rwlayer, err := layerStore.GetReferenceRWLayer(c.ContainerID)
	if err != nil {
		return nil, nil, err
	}
	// don't release rwlayer created in performcopy in case left building steps use copied/added data
	// defer func() {
	// 	if e := rwlayer.Unmount(); e != nil {
	// 		metadata, _ := i.layerStore.ReleaseRWLayer(rwlayer)
	// 		layer.LogReleaseMetadata(metadata)
	// 	}
	// }()

	stream, err := rwlayer.TarStream()
	if err != nil {
		return nil, nil, err
	}
	defer stream.Close()

	var parent *image.Image
	if c.ParentImageID == "" {
		parent = new(image.Image)
		parent.RootFS = image.NewRootFS()
	} else {
		parent, err = i.imageStore.Get(image.ID(c.ParentImageID))
		if err != nil {
			return nil, nil, err
		}
	}

	l, err := layerStore.Register(stream, parent.RootFS.ChainID())
	if err != nil {
		return nil, nil, err
	}

	newImage := image.NewChildImage(parent, image.ChildConfig{
		Author:          c.Author,
		ContainerConfig: c.ContainerConfig,
		DiffID:          l.DiffID(),
		Config:          c.Config,
	}, c.ContainerOS)

	config, err := newImage.MarshalJSON()
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to encode image config")
	}

	id, err := i.imageStore.Create(config)
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to create image")
	}

	if c.ParentImageID != "" {
		if err := i.imageStore.SetParent(id, image.ID(c.ParentImageID)); err != nil {
			return nil, nil, errors.Wrapf(err, "failed to set parent %s", parent)
		}
	}

	exportedImage, err := i.imageStore.Get(id)
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to export image")
	}

	return exportedImage, &roLayer{layerStore: layerStore, roLayer: l}, nil
}

func (i *ImageService) DeleteConfusedImages(images []image.ID) error {
	for k := len(images) - 1; k >= 0; k-- {
		id := images[k]
		i.imageStore.Delete(id)
		logrus.Infof("delete confused image id: %s", id)
	}
	return nil
}

func (i *ImageService) ReleaseRWLayers(layerNames []string, containerOS string) error {
	layerStore, ok := i.layerStores[containerOS]
	if !ok {
		return system.ErrNotSupportedOperatingSystem
	}

	for _, name := range layerNames {
		rwlayer, err := layerStore.GetReferenceRWLayer(name)
		if err != nil {
			return err
		}
		if err = rwlayer.Unmount(); err != nil {
			metadata, e := layerStore.ReleaseRWLayer(rwlayer)
			layer.LogReleaseMetadata(metadata)
			err = e
		}
		if err != nil {
			return err
		}
	}
	return nil
}
