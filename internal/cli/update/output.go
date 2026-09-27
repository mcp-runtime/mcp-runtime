package update

import (
	"fmt"
	"io"
	"text/tabwriter"
)

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func writePlanText(out io.Writer, plan *Plan) {
	fmt.Fprintln(out, "MCP Runtime platform update plan")
	fmt.Fprintf(out, "  Kube context:   %s\n", dash(plan.Cluster.Context))
	fmt.Fprintf(out, "  API server:     %s\n", dash(plan.Cluster.Server))
	fmt.Fprintf(out, "  Cluster ID:     %s\n", dash(plan.Cluster.ClusterID))
	fmt.Fprintf(out, "  Manifest:       %s\n", plan.ManifestSource)
	fmt.Fprintf(out, "  Version:        %s -> %s\n", dash(plan.InstalledVersion), plan.TargetVersion)
	if plan.ApplyCRDs {
		fmt.Fprintf(out, "  CRDs:           apply %s\n", stringsJoin(plan.CRDNames))
	} else if len(plan.CRDPreview) > 0 {
		fmt.Fprintf(out, "  CRDs:           all match release (skip apply)\n")
	}
	fmt.Fprintln(out)

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "COMPONENT\tWORKLOAD\tACTION\tCURRENT\tTARGET\tREASON")
	for _, r := range plan.Rows {
		workload := "-"
		if r.Deployment != "" {
			workload = r.Namespace + "/" + r.Deployment
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.Component, workload, r.Action, dash(r.CurrentImage), dash(r.TargetImage), dash(r.Reason))
		for _, n := range r.Notes {
			fmt.Fprintf(tw, "\t\t\tnote: %s\t\t\n", n)
		}
	}
	_ = tw.Flush()

	if len(plan.ImageBuilds) > 0 {
		fmt.Fprintln(out, "\nImages (--build):")
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "COMPONENT\tIMAGE\tACTION\tREASON")
		for _, a := range plan.ImageBuilds {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", a.Component, a.Image, a.Action, dash(a.Reason))
		}
		_ = tw.Flush()
	}
	if len(plan.CRDPreview) > 0 {
		fmt.Fprintln(out, "\nCustomResourceDefinitions:")
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "CRD\tACTION")
		for _, c := range plan.CRDPreview {
			fmt.Fprintf(tw, "%s\t%s\n", c.Name, dash(c.Action))
		}
		_ = tw.Flush()
	}

	if len(plan.Warnings) > 0 {
		fmt.Fprintln(out, "\nWarnings:")
		for _, w := range plan.Warnings {
			fmt.Fprintf(out, "  - %s\n", w)
		}
	}
	fmt.Fprintln(out, "\nPreserved (never modified by update):")
	for _, p := range plan.Preserved {
		fmt.Fprintf(out, "  - %s\n", p)
	}
}

func writeConfirmSummary(out io.Writer, plan *Plan, build bool) {
	fmt.Fprintln(out)
	if plan.ApplyCRDs {
		fmt.Fprintf(out, "This update will apply CustomResourceDefinitions (%s) before rolling images.\n", stringsJoin(plan.CRDNames))
	}
	if build && len(plan.ImageBuilds) > 0 {
		var builds, reuses int
		for _, a := range plan.ImageBuilds {
			if a.Action == ImageActionBuild {
				builds++
			} else {
				reuses++
			}
		}
		fmt.Fprintf(out, "Image builds: %d to build, %d to reuse from the registry.\n", builds, reuses)
	}
	for _, r := range plan.Changed() {
		if r.Component == "gateway-proxy" {
			fmt.Fprintln(out, "Note: gateway-proxy change restarts tenant MCP server pods.")
			break
		}
	}
}

func writeResultText(out io.Writer, res *Result) {
	fmt.Fprintln(out, "\nUpdate result:")
	if len(res.CRDs) > 0 {
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "CRD\tACTION\tDETAIL")
		for _, c := range res.CRDs {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", c.Name, dash(c.Action), dash(c.Error))
		}
		_ = tw.Flush()
		fmt.Fprintln(out)
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "WORKLOAD\tCOMPONENTS\tSTATUS\tDETAIL")
	for _, w := range res.Workloads {
		fmt.Fprintf(tw, "%s/%s\t%v\t%s\t%s\n", w.Namespace, w.Deployment, w.Components, w.Status, dash(w.Error))
	}
	_ = tw.Flush()
	if !res.Failed {
		return
	}
	fmt.Fprintln(out, "\nManual recovery commands (restore the images recorded before this update):")
	for _, w := range res.Workloads {
		if w.Status == StatusNotAttempted {
			continue
		}
		for _, c := range w.Recovery {
			fmt.Fprintf(out, "  %s\n", c)
		}
	}
}
