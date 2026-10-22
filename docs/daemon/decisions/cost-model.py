# Per-user monthly Cloudflare cost model for the Glitched hub (marginal, after account inclusions).
P = dict(do_req=0.15/1e6, gbs=12.50/1e6, row_w=1.00/1e6, do_gbmo=0.20, r2_gbmo=0.015,
         r2_a=4.50/1e6, wk_req=0.30/1e6)
GB = 0.125  # each DO billed at 128 MB
D = 30
tiers = {
 # thread-hours streaming/day, machines, remote PTY hours/day, live fraction
 "light":   dict(th=1,  machines=1, pty_h=0,  live=1.0),
 "typical": dict(th=6,  machines=2, pty_h=0.5,live=1.0),
 "heavy":   dict(th=40, machines=3, pty_h=2,  live=1.0),
}
def cost(t, batch_hz=1.0, handler_ms=10, items_per_th=200, rows_per_item=3,
         kb_per_th=300, hot_days=14, zstd=6, cap_interval_s=30):
    th = t["th"]*D
    live_msgs  = th*3600*batch_hz*t["live"]
    item_msgs  = th*items_per_th            # commits at item boundaries
    cap_msgs   = t["machines"]*D*86400/cap_interval_s
    pty_msgs   = t["pty_h"]*D*3600*20
    conn       = 50*D + 200*D               # ws connects + snapshot fetches
    in_msgs = live_msgs+item_msgs+cap_msgs+pty_msgs
    req = in_msgs/20 + conn
    gbs = (live_msgs*handler_ms + item_msgs*handler_ms + cap_msgs*5 + pty_msgs*3)/1000*GB
    rows_w = item_msgs*rows_per_item + 2*D*th/D*0  # metadata writes negligible
    hot_gb = th*kb_per_th/1e6 * hot_days/D
    r2_gb_added = th*kb_per_th/1e6/zstd     # grows each month
    c = dict(
      do_requests = req*P["do_req"],
      do_duration = gbs*P["gbs"],
      rows_written= rows_w*P["row_w"],
      do_storage  = hot_gb*P["do_gbmo"],
      r2_storage_after_12mo = 12*r2_gb_added*P["r2_gbmo"],
      workers     = conn*P["wk_req"],
    )
    c["total"] = sum(c.values())
    usage = dict(in_msgs=in_msgs, req=req, gbs=gbs, rows_w=rows_w, hot_gb=hot_gb, r2_gb_yr=12*r2_gb_added)
    return c, usage
for name,t in tiers.items():
    c,u = cost(t)
    print(f"\n== {name}")
    print("  usage:", {k: f"{v:,.0f}" if v>10 else f"{v:.3f}" for k,v in u.items()})
    print("  $:", {k: f"{v:.4f}" for k,v in c.items()})
# sensitivity: a DO that fails to hibernate, awake 24/7
print("\nstuck-awake DO per month: $", round(30*86400*GB*P["gbs"],2))
# heavy with 4 Hz live streaming and 3x bigger transcripts
c,_=cost(tiers["heavy"], batch_hz=4, kb_per_th=900); print("heavy, 4 Hz live, 900 KB/th:", {k: f"{v:.3f}" for k,v in c.items()})
# naive: one row per token-delta batch
c,_=cost(tiers["heavy"], items_per_th=3600, rows_per_item=3); print("heavy, row per 1s batch:", {k: f"{v:.3f}" for k,v in c.items()})
# included allowances cover how many typical users
_,u=cost(tiers["typical"])
print("typical users covered by inclusions: req",int(1e6/u["req"]),"gbs",int(4e5/u["gbs"]),"rows",int(50e6/u["rows_w"]),"storage",int(5/u["hot_gb"]))
# Sandbox VM: standard-1 (1/2 vCPU, 4 GiB, 8 GB), 10% active CPU
def vm(hours, vcpu=0.5, mem=4, disk=8, cpu_util=0.10):
    s=hours*3600
    return s*vcpu*cpu_util*0.000020 + s*mem*0.0000025 + s*disk*0.00000007
print("\nVM standard-1 per hour: $", round(vm(1),4), " | 4h/day month: $", round(vm(4*30),2), " | 10h/day x2 VMs: $", round(2*vm(10*30),2))
print("VM standard-1 per hour at 50% CPU: $", round(vm(1,cpu_util=0.5),4))
