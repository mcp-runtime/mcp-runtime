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
