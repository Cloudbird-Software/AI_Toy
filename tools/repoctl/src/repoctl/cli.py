"""repoctl CLI：元门禁六命令（spec §3.8）。退出码：0 通过 / 20 门禁红 / 2 配置或环境错误。"""
import argparse

from repoctl import affected, agentsmd, coverage, exemption, fetchmodels, forbidden


def _sub(parent, name, fn, *opts):
    p = parent.add_parser(name)
    for flag, default in opts:
        p.add_argument(flag, default=default)
    p.set_defaults(fn=fn)
    return p


def build_parser():
    p = argparse.ArgumentParser(prog="repoctl", description="元门禁（spec §3.8）")
    sub = p.add_subparsers(dest="cmd", required=True)
    _sub(sub, "coverage", coverage.run, ("--root", "."))
    _sub(sub, "forbidden-refs", forbidden.run, ("--root", "."))
    _sub(sub, "affected", affected.run, ("--root", ".")).add_argument("--base", required=True)
    m = _sub(sub, "fetch-models", fetchmodels.run, ("--manifest", "models/manifests"))
    m.add_argument("--cache", default="models/cache")
    a = _sub(sub, "agents-md", None)
    _sub(a.add_subparsers(dest="sub", required=True), "check", agentsmd.run, ("--root", "."))
    e = _sub(sub, "exemption", None)
    _sub(e.add_subparsers(dest="sub", required=True), "audit", exemption.run, ("--file", "reports/exemptions.yaml"))
    return p

def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    return args.fn(args)
