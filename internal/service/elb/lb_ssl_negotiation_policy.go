package elb

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/elb"
	"github.com/hashicorp/aws-sdk-go-base/v2/awsv1shim/v2/tfawserr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-provider-aws/internal/conns"
	"github.com/hashicorp/terraform-provider-aws/internal/create"
	"github.com/hashicorp/terraform-provider-aws/internal/errs/sdkdiag"
)

func ResourceSSLNegotiationPolicy() *schema.Resource {
	return &schema.Resource{
		// There is no concept of "updating" an LB policy in
		// the AWS API.
		CreateWithoutTimeout: resourceSSLNegotiationPolicyCreate,
		ReadWithoutTimeout:   resourceSSLNegotiationPolicyRead,
		DeleteWithoutTimeout: resourceSSLNegotiationPolicyDelete,

		Schema: map[string]*schema.Schema{
			"name": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},

			"load_balancer": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},

			"lb_port": {
				Type:     schema.TypeInt,
				Required: true,
				ForceNew: true,
			},

			"attribute": {
				Type:     schema.TypeSet,
				Optional: true,
				ForceNew: true,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"name": {
							Type:     schema.TypeString,
							Required: true,
						},

						"value": {
							Type:     schema.TypeString,
							Required: true,
						},
					},
				},
				Set: func(v interface{}) int {
					var buf bytes.Buffer
					m := v.(map[string]interface{})
					buf.WriteString(fmt.Sprintf("%s-", m["name"].(string)))
					return create.StringHashcode(buf.String())
				},
			},
		},
	}
}

func resourceSSLNegotiationPolicyCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	var diags diag.Diagnostics
	conn := meta.(*conns.AWSClient).ELBConn()

	// Provision the SSLNegotiationPolicy
	lbspOpts := &elb.CreateLoadBalancerPolicyInput{
		LoadBalancerName: aws.String(d.Get("load_balancer").(string)),
		PolicyName:       aws.String(d.Get("name").(string)),
		PolicyTypeName:   aws.String("SSLNegotiationPolicyType"),
	}

	// Check for Policy Attributes
	if v, ok := d.GetOk("attribute"); ok {
		// Expand the "attribute" set to aws-sdk-go compat []*elb.PolicyAttribute
		lbspOpts.PolicyAttributes = ExpandPolicyAttributes(v.(*schema.Set).List())
	}

	log.Printf("[DEBUG] Load Balancer Policy opts: %#v", lbspOpts)
	if _, err := conn.CreateLoadBalancerPolicyWithContext(ctx, lbspOpts); err != nil {
		return sdkdiag.AppendErrorf(diags, "creating Load Balancer Policy: %s", err)
	}

	setLoadBalancerOpts := &elb.SetLoadBalancerPoliciesOfListenerInput{
		LoadBalancerName: aws.String(d.Get("load_balancer").(string)),
		LoadBalancerPort: aws.Int64(int64(d.Get("lb_port").(int))),
		PolicyNames:      []*string{aws.String(d.Get("name").(string))},
	}

	log.Printf("[DEBUG] SSL Negotiation create configuration: %#v", setLoadBalancerOpts)
	if _, err := conn.SetLoadBalancerPoliciesOfListenerWithContext(ctx, setLoadBalancerOpts); err != nil {
		return sdkdiag.AppendErrorf(diags, "setting SSLNegotiationPolicy: %s", err)
	}

	d.SetId(fmt.Sprintf("%s:%d:%s",
		*lbspOpts.LoadBalancerName,
		*setLoadBalancerOpts.LoadBalancerPort,
		*lbspOpts.PolicyName))
	return diags
}

func resourceSSLNegotiationPolicyRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	var diags diag.Diagnostics
	conn := meta.(*conns.AWSClient).ELBConn()

	lbName, lbPort, policyName, err := SSLNegotiationPolicyParseID(d.Id())
	if err != nil {
		return sdkdiag.AppendErrorf(diags, "reading ELB Classic (%s) SSL Negotiation Policy: %s", lbName, err)
	}

	request := &elb.DescribeLoadBalancerPoliciesInput{
		LoadBalancerName: aws.String(lbName),
		PolicyNames:      []*string{aws.String(policyName)},
	}

	getResp, err := conn.DescribeLoadBalancerPoliciesWithContext(ctx, request)
	if err != nil {
		if !d.IsNewResource() && tfawserr.ErrCodeEquals(err, elb.ErrCodePolicyNotFoundException) {
			log.Printf("[WARN] ELB Classic LB (%s) policy (%s) not found, removing from state", lbName, policyName)
			d.SetId("")
			return diags
		} else if !d.IsNewResource() && tfawserr.ErrCodeEquals(err, elb.ErrCodeAccessPointNotFoundException) {
			log.Printf("[WARN] ELB Classic LB (%s) not found, removing from state", lbName)
			d.SetId("")
			return diags
		}
		return sdkdiag.AppendErrorf(diags, "reading ELB Classic (%s) SSL Negotiation Policy: %s", lbName, err)
	}

	if len(getResp.PolicyDescriptions) != 1 {
		return sdkdiag.AppendErrorf(diags, "Unable to find policy %#v", getResp.PolicyDescriptions)
	}

	d.Set("name", policyName)
	d.Set("load_balancer", lbName)
	d.Set("lb_port", lbPort)

	// TODO: fix attribute
	// This was previously erroneously setting "attributes", however this cannot
	// be changed without introducing problematic side effects. The ELB service
	// automatically expands the results to include all SSL attributes
	// (unordered, so we'd need to switch to TypeSet anyways), which we would be
	// quite impractical to force practitioners to write out and potentially
	// update each time the API updates since there is nearly 100 attributes.

	// We can get away with this because there's only one policy returned
	// policyDesc := getResp.PolicyDescriptions[0]
	// attributes := FlattenPolicyAttributes(policyDesc.PolicyAttributeDescriptions)
	// if err := d.Set("attribute", attributes); err != nil {
	// 	return fmt.Errorf("setting attribute: %s", err)
	// }

	return diags
}

func resourceSSLNegotiationPolicyDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	var diags diag.Diagnostics
	conn := meta.(*conns.AWSClient).ELBConn()

	lbName, _, policyName, err := SSLNegotiationPolicyParseID(d.Id())
	if err != nil {
		return sdkdiag.AppendErrorf(diags, "deleting ELB Classic SSL Negotiation Policy (%s): %s", d.Id(), err)
	}

	// Perversely, if we Set an empty list of PolicyNames, we detach the
	// policies attached to a listener, which is required to delete the
	// policy itself.
	setLoadBalancerOpts := &elb.SetLoadBalancerPoliciesOfListenerInput{
		LoadBalancerName: aws.String(d.Get("load_balancer").(string)),
		LoadBalancerPort: aws.Int64(int64(d.Get("lb_port").(int))),
		PolicyNames:      []*string{},
	}

	if _, err := conn.SetLoadBalancerPoliciesOfListenerWithContext(ctx, setLoadBalancerOpts); err != nil {
		return sdkdiag.AppendErrorf(diags, "removing SSLNegotiationPolicy: %s", err)
	}

	request := &elb.DeleteLoadBalancerPolicyInput{
		LoadBalancerName: aws.String(lbName),
		PolicyName:       aws.String(policyName),
	}

	if _, err := conn.DeleteLoadBalancerPolicyWithContext(ctx, request); err != nil {
		return sdkdiag.AppendErrorf(diags, "deleting ELB Classic SSL Negotiation Policy (%s): %s", d.Id(), err)
	}
	return diags
}

// SSLNegotiationPolicyParseID takes an ID and parses it into
// it's constituent parts. You need three axes (LB name, policy name, and LB
// port) to create or identify an SSL negotiation policy in AWS's API.
func SSLNegotiationPolicyParseID(id string) (string, int, string, error) {
	const partCount = 3
	parts := strings.SplitN(id, ":", partCount)
	if n := len(parts); n != partCount {
		return "", 0, "", fmt.Errorf("incorrect format of SSL negotiation policy resource ID. Expected %d parts, got %d", partCount, n)
	}

	port, err := strconv.Atoi(parts[1])
	if err != nil {
		return "", 0, "", fmt.Errorf("parsing SSL negotiation policy resource ID port: %w", err)
	}

	return parts[0], port, parts[2], nil
}
