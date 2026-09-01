import { useEffect } from "preact/hooks";
import { api } from "../lib/api";
import { useLoad } from "../lib/useLoad";
import { accountFilter, accountQS, setList } from "../lib/store";
import { decide, undecide, BUCKET_LABEL } from "../lib/actions";
import type { Bucket, Person } from "../lib/types";
import { countOf, fmtDate, splitFrom } from "../lib/fmt";
import { Avatar, Empty, ListSkeleton, PageHead } from "../ui/bits";

const ROUTES: Bucket[] = ["imbox", "feed", "paper_trail"];

function PersonRow({ person }: { person: Person }) {
  const who = splitFrom(person.sender);
  const blocked = !person.allowed;

  return (
    <div class="people-row">
      <Avatar email={who.email} name={who.name} size="sm" />
      <div class="people-main">
        <div class="people-name">{who.name || who.email}</div>
        <div class="people-note">
          {countOf(person.total, "message")}
          {person.last_at ? " · last " + fmtDate(person.last_at) : ""}
          {person.last_subject ? " · " + person.last_subject : ""}
        </div>
      </div>

      <div class="people-acts" role="group" aria-label={"Routing for " + (who.name || who.email)}>
        {blocked ? (
          <span class="chip chip-danger">Blocked</span>
        ) : (
          // Re-routing here decides where their *future* mail lands; existing
          // threads keep whatever bucket they are in.
          ROUTES.map((r) => (
            <button
              key={r} type="button"
              class={"btn btn-sm " + (person.route === r ? "btn-primary" : "btn-ghost")}
              title={"Send future mail to " + BUCKET_LABEL[r]}
              aria-pressed={person.route === r}
              onClick={() => person.route !== r && decide(person.sender, true, r)}
            >
              {BUCKET_LABEL[r]}
            </button>
          ))
        )}
        {/* The only way back out of a decision — blocking used to be permanent. */}
        <button
          class="btn btn-ghost btn-sm" type="button"
          title="Return this sender to the Screener"
          onClick={() => undecide(person.sender)}
        >
          {blocked ? "Unblock" : "Reset"}
        </button>
      </div>
    </div>
  );
}

export function PeopleView() {
  const lens = accountFilter.value;
  const { data, loading, error } = useLoad<Person[]>("people:" + lens, (signal) =>
    api<Person[]>(accountQS("/people"), { signal })
  );

  useEffect(() => {
    setList({ kind: "none", key: "", loading, error, rows: [], senders: [], origin: null });
  }, [loading, error]);

  return (
    <>
      <PageHead
        title="People"
        sub="Everyone you've decided about, and where their mail goes next."
      />
      {loading && !data && <ListSkeleton rows={5} />}
      {error && <Empty title="That didn't load." sub={error} />}
      {data && data.length === 0 && !loading && (
        <Empty
          title="No one yet."
          sub="Screen a sender and they'll appear here, with everything you've exchanged."
        />
      )}
      {data?.map((p) => <PersonRow person={p} key={p.sender} />)}
    </>
  );
}
