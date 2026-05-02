import asyncio
import copy
import json
import os
import random
import re
import sys
import time
import uuid
from datetime import datetime, timezone
from email.utils import parsedate_to_datetime
from pathlib import Path
from urllib.parse import unquote, urlparse

import aiohttp
from bs4 import BeautifulSoup

BASE_MATCH_URL = "https://www.ubisoft.com/en-us/esports/rainbow-six/siege/match"
START_MATCH_ID = 6000
CHUNK_SIZE = 1024 * 1024
DOWNLOAD_LOG_INTERVAL_BYTES = 64 * 1024 * 1024
PROGRESS_PATH = Path(__file__).with_name("replay_progress.json")
MAX_CONSECUTIVE_404S = 1000
MAX_PAGE_500_ERRORS = 50
PAGE_CONCURRENCY = 8
DOWNLOAD_CONCURRENCY = 3
SAVE_INTERVAL_SECONDS = 5
MAX_429_RETRIES = 6
BACKOFF_BASE_SECONDS = 1
BACKOFF_MAX_SECONDS = 60
REQUEST_FAILED = object()
REQUEST_500_FAILED = object()
headers = {
    "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:150.0) Gecko/20100101 Firefox/150.0",
    "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
    "Accept-Language": "en-US,en;q=0.9",
    "Sec-GPC": "1",
    "Alt-Used": "www.ubisoft.com",
    "Connection": "keep-alive",
    "Cookie": "OptanonConsent=isGpcEnabled=1&datestamp=Fri+Mar+13+2026+20%3A02%3A23+GMT-0400+(Eastern+Daylight+Time)&version=202601.1.0&browserGpcFlag=1&isIABGlobal=false&consentId=2f9c1782-a0da-4a89-b730-cc56ec5dd68b&interactionCount=2&isAnonUser=1&intType=1&hosts=&landingPath=NotLandingPage&groups=C0001%3A1%2CC0002%3A1%2CC0003%3A1%2CC0004%3A1%2CC0005%3A1&crTime=1773446543076; UBI_PRIVACY_AA_OPTOUT=false; UBI_PRIVACY_ADS_OPTOUT=false; UBI_PRIVACY_CUSTOMIZATION_OPTOUT=false; UBI_PRIVACY_VIDEO_OPTOUT=false; OptanonAlertBoxClosed=2026-03-14T00:02:22.899Z; _gcl_au=1.1.252373136.1773446543; UBI_PRIVACY_POLICY_ACCEPTED=true; UBI_PRIVACY_POLICY_VIEWED=true; _ALGOLIA=anonymous-cbce1091-d04b-4b1d-a19b-ab968a898206; AMCV_29E26A9C57069D117F000101%40AdobeOrg=1585540135%7CMCIDTS%7C20576%7CMCMID%7C39443744758526741012392454835785259085%7CMCAID%7CNONE%7CMCOPTOUT-1773453743s%7CNONE%7CvVersion%7C4.4.0; _scid=TPpKdXUjAIusq5sqogqCp2cPBvlrBwrY; _scid_r=TPpKdXUjAIusq5sqogqCp2cPBvlrBwrY; _sctr=1%7C1773374400000; datadome=tylzCgbu64YZORL3yBcA5HOTzInidCCyf8myMl37EaqA3BrHKQMiM4fdvSU~gckM5l0deFHb2bSNcVwyuhCNJ0AIShk~18b4rjOWut6K9bFkgjmV2IWuPXIeqa4_0vI3; UBI_PRIVACY_US_CMP=true; ubisoft_ua=NDQ=; AMCVS_29E26A9C57069D117F000101%40AdobeOrg=1; s_sess=%20s_cc%3Dtrue%3B%20s_sq%3Dubisoftintglobalprod%253D%252526c.%252526a.%252526activitymap.%252526page%25253Dmatch%25252520details%252526link%25253DReplay%252526region%25253Dapp%252526pageIDType%25253D1%252526.activitymap%252526.a%252526.c%3B; gpv=match%20details",
    "Upgrade-Insecure-Requests": "1",
    "Sec-Fetch-Dest": "document",
    "Sec-Fetch-Mode": "navigate",
    "Sec-Fetch-Site": "none",
    "Sec-Fetch-User": "?1",
    "Priority": "u=0, i",
    "TE": "trailers",
}


def find_key(data, key):
    if isinstance(data, dict):
        value = data.get(key)
        if value:
            return value

        for item in data.values():
            found = find_key(item, key)
            if found:
                return found

    if isinstance(data, list):
        for item in data:
            found = find_key(item, key)
            if found:
                return found

    return None


def extract_replay_link(html):
    soup = BeautifulSoup(html, "html.parser")
    next_data = soup.find("script", id="__NEXT_DATA__")

    if next_data and next_data.string:
        data = json.loads(next_data.string)
        replay_link = find_key(data, "replayLink")
        if replay_link:
            return replay_link

    match = re.search(r'"replayLink"\s*:\s*"([^"]+)"', html)
    if match:
        return match.group(1).encode("utf-8").decode("unicode_escape")

    raise ValueError("Could not find a replayLink in the HTML response.")


def filename_from_url(download_url):
    path = urlparse(download_url).path
    filename = unquote(Path(path).name)
    if not filename:
        raise ValueError(f"Could not determine a filename from {download_url}")
    return filename


def utc_now():
    return datetime.now(timezone.utc).isoformat()


def format_bytes(size):
    for unit in ("B", "KB", "MB", "GB"):
        if size < 1024 or unit == "GB":
            return f"{size:.1f} {unit}"

        size /= 1024


def make_progress():
    return {
        "startedAt": utc_now(),
        "updatedAt": utc_now(),
        "nextMatchId": START_MATCH_ID,
        "lastProcessedMatchId": None,
        "consecutive404s": 0,
        "counts": {
            "matchesProcessed": 0,
            "matches404": 0,
            "pageFailures": 0,
            "page500Failures": 0,
            "matchesWithoutReplay": 0,
            "replayLinksFound": 0,
            "uniqueReplayLinks": 0,
            "downloadsSucceeded": 0,
            "downloadsFailed": 0,
        },
        "matches": {},
        "replayLinks": {},
    }


def load_progress(path):
    if not path.exists():
        return make_progress()

    with path.open("r", encoding="utf-8") as file:
        progress = json.load(file)

    progress.setdefault("counts", {})
    progress.setdefault("matches", {})
    progress.setdefault("replayLinks", {})
    progress.setdefault("nextMatchId", START_MATCH_ID)
    progress.setdefault("lastProcessedMatchId", None)
    progress.setdefault("consecutive404s", 0)
    progress["counts"].setdefault("page500Failures", 0)
    return progress


def save_progress(path, progress):
    progress["updatedAt"] = utc_now()
    path.parent.mkdir(parents=True, exist_ok=True)
    temp_path = path.with_name(f"{path.name}.{os.getpid()}.{uuid.uuid4().hex}.tmp")

    with temp_path.open("w", encoding="utf-8") as file:
        json.dump(progress, file, indent=2, sort_keys=True)

    for attempt in range(1, 6):
        try:
            temp_path.replace(path)
            return
        except PermissionError as error:
            if attempt == 5:
                print(f"Could not save progress to {path}: {error}")
                print(f"Latest progress snapshot left at {temp_path}")
                return

            time.sleep(0.2 * attempt)


class ProgressSaver:
    def __init__(self, path, progress, lock):
        self.path = path
        self.progress = progress
        self.lock = lock
        self.dirty = False
        self.stopping = False
        self.wake_event = None
        self.task = None

    def start(self):
        self.wake_event = asyncio.Event()
        self.task = asyncio.create_task(self.run())

    def mark_dirty(self):
        self.dirty = True

    async def flush(self):
        if not self.dirty:
            return

        async with self.lock:
            snapshot = copy.deepcopy(self.progress)
            self.dirty = False

        await asyncio.to_thread(save_progress, self.path, snapshot)

    async def run(self):
        while True:
            try:
                await asyncio.wait_for(
                    self.wake_event.wait(),
                    timeout=SAVE_INTERVAL_SECONDS,
                )
            except asyncio.TimeoutError:
                pass

            self.wake_event.clear()

            if self.dirty:
                await self.flush()

            if self.stopping and not self.dirty:
                return

    async def stop(self):
        self.stopping = True

        if self.wake_event:
            self.wake_event.set()

        if self.task:
            await self.task

        await self.flush()


def increment_count(progress, key):
    progress["counts"][key] = progress["counts"].get(key, 0) + 1


async def record_match_progress(
    progress, lock, progress_saver, match_id, status, replay_link=None, error=None
):
    async with lock:
        match = {
            "matchId": match_id,
            "url": f"{BASE_MATCH_URL}/{match_id}",
            "status": status,
            "checkedAt": utc_now(),
        }

        if replay_link:
            match["replayLink"] = replay_link

        if error:
            match["error"] = str(error)

        progress["matches"][str(match_id)] = match
        progress["lastProcessedMatchId"] = max(
            progress.get("lastProcessedMatchId") or 0,
            match_id,
        )
        progress["nextMatchId"] = max(
            progress.get("nextMatchId", START_MATCH_ID),
            match_id + 1,
        )
        increment_count(progress, "matchesProcessed")

        if status == "404":
            increment_count(progress, "matches404")
        elif status == "page_failed":
            increment_count(progress, "pageFailures")
        elif status == "page_500":
            increment_count(progress, "pageFailures")
            increment_count(progress, "page500Failures")
        elif status == "no_replay":
            increment_count(progress, "matchesWithoutReplay")
        elif status == "replay_found":
            increment_count(progress, "replayLinksFound")

            if replay_link not in progress["replayLinks"]:
                progress["replayLinks"][replay_link] = {
                    "url": replay_link,
                    "firstMatchId": match_id,
                    "matchIds": [],
                    "status": "found",
                    "foundAt": utc_now(),
                }
                progress["counts"]["uniqueReplayLinks"] = len(progress["replayLinks"])

            if match_id not in progress["replayLinks"][replay_link]["matchIds"]:
                progress["replayLinks"][replay_link]["matchIds"].append(match_id)

        progress_saver.mark_dirty()


async def record_download_progress(
    progress,
    lock,
    progress_saver,
    replay_link,
    status,
    output_path=None,
    error=None,
):
    async with lock:
        replay = progress["replayLinks"].setdefault(
            replay_link,
            {
                "url": replay_link,
                "firstMatchId": None,
                "matchIds": [],
                "status": "found",
                "foundAt": utc_now(),
            },
        )

        replay["status"] = status
        replay["updatedAt"] = utc_now()

        if output_path:
            replay["outputPath"] = str(output_path)

        if error:
            replay["error"] = str(error)

        if status == "downloaded":
            increment_count(progress, "downloadsSucceeded")
        elif status == "download_failed":
            increment_count(progress, "downloadsFailed")

        progress["counts"]["uniqueReplayLinks"] = len(progress["replayLinks"])
        progress_saver.mark_dirty()


def retry_after_seconds(response):
    retry_after = response.headers.get("Retry-After")
    if not retry_after:
        return None

    try:
        return max(0, int(retry_after))
    except ValueError:
        pass

    try:
        retry_after_date = parsedate_to_datetime(retry_after)
    except (TypeError, ValueError):
        return None

    if retry_after_date.tzinfo is None:
        retry_after_date = retry_after_date.replace(tzinfo=timezone.utc)

    delay = (retry_after_date - datetime.now(timezone.utc)).total_seconds()
    return max(0, delay)


async def sleep_after_429(label, attempt, response):
    retry_after = retry_after_seconds(response)

    if retry_after is None:
        retry_after = min(
            BACKOFF_BASE_SECONDS * 2 ** (attempt - 1),
            BACKOFF_MAX_SECONDS,
        )
        retry_after += random.uniform(0, 0.5)

    print(f"{label}: HTTP 429, retrying in {retry_after:.1f}s")
    await asyncio.sleep(retry_after)


async def fetch_match_html(session, match_url):
    for attempt in range(1, MAX_429_RETRIES + 1):
        async with session.get(match_url) as response:
            if response.status == 404:
                return None

            if response.status == 429 and attempt < MAX_429_RETRIES:
                await sleep_after_429(match_url, attempt, response)
                continue

            response.raise_for_status()
            return await response.text()

    raise RuntimeError(f"{match_url}: exceeded 429 retry limit")


async def download_file(session, download_url, output_dir, label):
    output_dir.mkdir(parents=True, exist_ok=True)
    output_path = output_dir / filename_from_url(download_url)
    temp_path = output_path.with_suffix(output_path.suffix + ".part")

    if output_path.exists() and output_path.stat().st_size > 0:
        return output_path

    last_error = None
    for attempt in range(1, MAX_429_RETRIES + 1):
        try:
            async with session.get(download_url) as response:
                if response.status == 429 and attempt < MAX_429_RETRIES:
                    await sleep_after_429(download_url, attempt, response)
                    continue

                response.raise_for_status()
                content_length = response.content_length
                downloaded = 0
                next_log_at = DOWNLOAD_LOG_INTERVAL_BYTES

                if content_length:
                    print(
                        f"{label}: downloading {format_bytes(content_length)} "
                        f"to {output_path.name}"
                    )

                with temp_path.open("wb") as file:
                    async for chunk in response.content.iter_chunked(CHUNK_SIZE):
                        if chunk:
                            file.write(chunk)
                            downloaded += len(chunk)

                            if downloaded >= next_log_at:
                                if content_length:
                                    percent = downloaded / content_length * 100
                                    print(
                                        f"{label}: {format_bytes(downloaded)} / "
                                        f"{format_bytes(content_length)} "
                                        f"({percent:.1f}%)"
                                    )
                                else:
                                    print(f"{label}: {format_bytes(downloaded)}")

                                next_log_at += DOWNLOAD_LOG_INTERVAL_BYTES

                temp_path.replace(output_path)
                return output_path
        except Exception as error:
            last_error = error
            if temp_path.exists():
                temp_path.unlink()

            if attempt >= MAX_429_RETRIES:
                raise

            delay = min(
                BACKOFF_BASE_SECONDS * 2 ** (attempt - 1),
                BACKOFF_MAX_SECONDS,
            )
            delay += random.uniform(0, 0.5)
            print(
                f"{download_url}: download interrupted ({error}), "
                f"retrying in {delay:.1f}s"
            )
            await asyncio.sleep(delay)

    raise RuntimeError(f"{download_url}: exceeded retry limit") from last_error


async def scrape_match(session, match_id):
    match_url = f"{BASE_MATCH_URL}/{match_id}"

    try:
        html = await fetch_match_html(session, match_url)
    except asyncio.CancelledError:
        raise
    except aiohttp.ClientResponseError as error:
        if error.status == 500:
            print(f"Match {match_id}: HTTP 500 page request failed")
            return REQUEST_500_FAILED, str(error)

        print(f"Match {match_id}: page request failed, continuing: {error}")
        return REQUEST_FAILED, str(error)
    except Exception as error:
        print(f"Match {match_id}: page request failed, continuing: {error}")
        return REQUEST_FAILED, str(error)

    if html is None:
        return None, None

    try:
        replay_link = extract_replay_link(html)
    except ValueError:
        print(f"Match {match_id}: no replay link found")
        return "", None

    print(f"Match {match_id}: found replay link: {replay_link}")
    return replay_link, None


async def download_replay(
    session,
    progress,
    progress_lock,
    progress_saver,
    match_id,
    replay_link,
    output_dir,
):
    print(f"Match {match_id}: downloading replay")
    try:
        output_path = await download_file(
            session,
            replay_link,
            output_dir,
            f"Match {match_id}",
        )
    except Exception as error:
        print(f"Match {match_id}: download failed, continuing: {error}")
        await record_download_progress(
            progress,
            progress_lock,
            progress_saver,
            replay_link,
            "download_failed",
            error=error,
        )
        return

    print(f"Match {match_id}: downloaded replay to: {output_path}")
    await record_download_progress(
        progress,
        progress_lock,
        progress_saver,
        replay_link,
        "downloaded",
        output_path=output_path,
    )


async def download_worker(
    name,
    session,
    queue,
    progress,
    progress_lock,
    progress_saver,
    output_dir,
):
    while True:
        item = await queue.get()
        try:
            if item is None:
                print(f"{name}: stopped")
                return

            match_id, replay_link = item
            await download_replay(
                session,
                progress,
                progress_lock,
                progress_saver,
                match_id,
                replay_link,
                output_dir,
            )
        finally:
            queue.task_done()


async def queue_download(queue, scheduled_downloads, match_id, replay_link):
    if replay_link in scheduled_downloads:
        return

    scheduled_downloads.add(replay_link)
    queue.put_nowait((match_id, replay_link))
    print(f"Match {match_id}: queued replay download")


async def queue_pending_downloads(progress, queue, scheduled_downloads):
    queued = 0

    for replay_link, replay in progress.get("replayLinks", {}).items():
        if replay.get("status") == "downloaded":
            continue

        match_id = replay.get("firstMatchId")
        await queue_download(queue, scheduled_downloads, match_id, replay_link)
        queued += 1

    if queued:
        print(f"Queued {queued} pending replay download(s) from progress")


def schedule_match(fetch_tasks, session, match_id):
    print(f"Checking match {match_id}")
    fetch_tasks[match_id] = asyncio.create_task(scrape_match(session, match_id))


async def cancel_tasks(tasks):
    for task in tasks:
        task.cancel()

    try:
        await asyncio.gather(*tasks, return_exceptions=True)
    except ValueError:
        pass


async def main():
    output_dir = Path(__file__).parent / "replays"
    progress = load_progress(PROGRESS_PATH)
    progress_lock = asyncio.Lock()
    next_match_id = max(START_MATCH_ID, progress.get("nextMatchId", START_MATCH_ID))
    consecutive_404s = progress.get("consecutive404s", 0)
    page_500_errors = 0
    fetch_tasks = {}
    download_queue = asyncio.Queue()
    scheduled_downloads = set()
    progress_saver = ProgressSaver(PROGRESS_PATH, progress, progress_lock)
    timeout = aiohttp.ClientTimeout(total=None, sock_connect=60, sock_read=120)

    await asyncio.to_thread(save_progress, PROGRESS_PATH, progress)
    print(f"Saving progress to: {PROGRESS_PATH}")
    progress_saver.start()

    async with aiohttp.ClientSession(headers=headers, timeout=timeout) as session:
        download_workers = [
            asyncio.create_task(
                download_worker(
                    f"download-{worker_id}",
                    session,
                    download_queue,
                    progress,
                    progress_lock,
                    progress_saver,
                    output_dir,
                )
            )
            for worker_id in range(1, DOWNLOAD_CONCURRENCY + 1)
        ]

        await queue_pending_downloads(progress, download_queue, scheduled_downloads)

        for _ in range(PAGE_CONCURRENCY):
            schedule_match(fetch_tasks, session, next_match_id)
            next_match_id += 1

        try:
            while True:
                done, _ = await asyncio.wait(
                    fetch_tasks.values(),
                    return_when=asyncio.FIRST_COMPLETED,
                )

                for task in done:
                    match_id = None

                    for task_match_id, fetch_task in fetch_tasks.items():
                        if fetch_task is task:
                            match_id = task_match_id
                            break

                    if match_id is None:
                        continue

                    del fetch_tasks[match_id]
                    replay_link, error = await task

                    if replay_link is None:
                        consecutive_404s += 1
                        progress["consecutive404s"] = consecutive_404s
                        await record_match_progress(
                            progress,
                            progress_lock,
                            progress_saver,
                            match_id,
                            "404",
                        )
                        print(
                            f"Match {match_id}: 404 received "
                            f"({consecutive_404s}/{MAX_CONSECUTIVE_404S})"
                        )

                        if consecutive_404s >= MAX_CONSECUTIVE_404S:
                            print(
                                f"Stopping after {MAX_CONSECUTIVE_404S} "
                                "consecutive 404s"
                            )
                            await cancel_tasks(fetch_tasks.values())
                            return
                    else:
                        if replay_link not in (REQUEST_FAILED, REQUEST_500_FAILED):
                            consecutive_404s = 0
                            progress["consecutive404s"] = consecutive_404s

                        if replay_link is REQUEST_500_FAILED:
                            page_500_errors += 1
                            await record_match_progress(
                                progress,
                                progress_lock,
                                progress_saver,
                                match_id,
                                "page_500",
                                error=error,
                            )
                            print(
                                f"Match {match_id}: HTTP 500 received "
                                f"({page_500_errors}/{MAX_PAGE_500_ERRORS})"
                            )

                            if page_500_errors >= MAX_PAGE_500_ERRORS:
                                print(
                                    f"Stopping match indexing after "
                                    f"{MAX_PAGE_500_ERRORS} HTTP 500 errors"
                                )
                                await cancel_tasks(fetch_tasks.values())
                                return
                        elif replay_link is REQUEST_FAILED:
                            await record_match_progress(
                                progress,
                                progress_lock,
                                progress_saver,
                                match_id,
                                "page_failed",
                                error=error,
                            )
                        elif replay_link == "":
                            await record_match_progress(
                                progress,
                                progress_lock,
                                progress_saver,
                                match_id,
                                "no_replay",
                            )
                        else:
                            await record_match_progress(
                                progress,
                                progress_lock,
                                progress_saver,
                                match_id,
                                "replay_found",
                                replay_link=replay_link,
                            )

                        if isinstance(replay_link, str) and replay_link:
                            await queue_download(
                                download_queue,
                                scheduled_downloads,
                                match_id,
                                replay_link,
                            )

                    schedule_match(fetch_tasks, session, next_match_id)
                    next_match_id += 1
        finally:
            try:
                if fetch_tasks:
                    await cancel_tasks(fetch_tasks.values())

                if not download_queue.empty():
                    print(
                        f"Waiting for {download_queue.qsize()} queued replay "
                        "download(s) to finish"
                    )

                await download_queue.join()

                for _ in download_workers:
                    await download_queue.put(None)

                await asyncio.gather(*download_workers)
            finally:
                await progress_saver.stop()


if __name__ == "__main__":
    if sys.platform == "win32":
        asyncio.set_event_loop_policy(asyncio.WindowsSelectorEventLoopPolicy())

    asyncio.run(main())
