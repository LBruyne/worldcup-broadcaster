import json, urllib.request, datetime, sys
SLUGS = ["world-cup-winner","world-cup-team-to-advance-to-knockout-stages",
         "world-cup-golden-boot-winner","which-continent-will-win-the-world-cup"] + \
        [f"world-cup-group-{c}-winner" for c in "abcdefghijkl"]
LABELS = {"world-cup-winner":"夺冠概率","world-cup-team-to-advance-to-knockout-stages":"出线(进淘汰赛)概率",
          "world-cup-golden-boot-winner":"金靴概率","which-continent-will-win-the-world-cup":"夺冠大洲概率"}
def fetch(slug):
    req=urllib.request.Request(f"https://gamma-api.polymarket.com/events?slug={slug}",headers={"User-Agent":"wcb/1.0"})
    with urllib.request.urlopen(req,timeout=15) as r: return json.load(r)
out={"source":"Polymarket实时盘口(prediction market隐含概率)","updated":datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%MZ"),"markets":{}}
for slug in SLUGS:
    try:
        evs=fetch(slug)
        if not evs: continue
        e=evs[0]; rows=[]
        for m in e.get("markets",[]):
            try:
                op=json.loads(m.get("outcomePrices","[]")); name=m.get("groupItemTitle") or m.get("question")
                if op and name and float(op[0])>=0.005: rows.append({"name":name,"prob":round(float(op[0]),4)})
            except: pass
        rows.sort(key=lambda x:-x["prob"])
        label=LABELS.get(slug) or e.get("title")
        if slug.startswith("world-cup-group-"): label="小组头名_"+slug.split('-')[-2].upper()+"组"
        if rows: out["markets"][label]=rows[:16]
    except Exception: pass
print(json.dumps(out,ensure_ascii=False))
