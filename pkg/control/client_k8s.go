package control

import (
	context "context"
	"math/rand"
	"strings"
	"time"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

func (c *Client) ConnectToKubernetes() error {
	cfg, err := K8SConfig("")
	if err != nil {
		return err
	}

	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}

	c.clientset = cs

	return nil
}

func (c *Client) checkImageTag(ctx context.Context, newTag string, delay bool) error {
	if c.clientset == nil || c.cfg.K8Deployment == "" {
		c.L.Debug("no kubernetes configuration set, ignoring tag", "tag", newTag)
		return nil
	}

	depapi := c.clientset.AppsV1().Deployments("default")
	dep, err := depapi.Get(ctx, c.cfg.K8Deployment, v1.GetOptions{})
	if err != nil {
		c.L.Error("error fetching deployment for image update", "error", err, "deployment", c.cfg.K8Deployment)
		return err
	}

	img := dep.Spec.Template.Spec.Containers[0].Image

	var repo string

	idx := strings.LastIndexByte(img, ':')
	if idx == -1 {
		repo = img
	} else {
		repo = img[:idx]
		if img[idx+1:] == newTag {
			c.L.Trace("deployment already at desired tag, skipping")
			return nil
		}
	}

	newImage := repo + ":" + newTag

	dep.Spec.Template.Spec.Containers[0].Image = newImage

	if delay {
		secs := rand.Int31n(10)

		c.L.Info("delaying before updating deployment", "seconds", secs)
		time.Sleep(time.Duration(secs) * time.Second)
	}

	c.L.Info("updating deployment", "image", newImage, "prev", img)

	_, err = depapi.Update(ctx, dep, v1.UpdateOptions{})
	return err
}
