package twitch

import "fmt"

// jsSeedPlayer returns a script that seeds Twitch's player localStorage keys so
// the next navigation starts at low (non-zero) volume, unmuted, at the lowest
// quality. Volume must never be 0: Chrome 145+ pauses a muted video when its
// tab is backgrounded, which would silently stop drop credit.
// verify live: exact key names ("volume", "video-muted", "video-quality") and
// their value encodings.
func jsSeedPlayer(volume float64, lowestQuality bool) string {
	quality := ""
	if lowestQuality {
		// Twitch stores quality as a JSON object; "160p30" is the lowest common
		// group. Also set the group to "160p" as a fallback key. verify live.
		quality = `try { localStorage.setItem('video-quality', JSON.stringify({default:"160p30"})); } catch(e){}`
	}
	return fmt.Sprintf(`(() => {
  try { localStorage.setItem('volume', '%.4f'); } catch(e){}
  try { localStorage.setItem('video-muted', JSON.stringify({default:false})); } catch(e){}
  %s
})()`, volume, quality)
}

// jsReassertVolume forces the live <video> element back to policy volume and
// unmutes it, for the periodic reassert timer.
func jsReassertVolume(volume float64) string {
	return fmt.Sprintf(`(() => {
  const v = document.querySelector('video');
  if (!v) return false;
  v.muted = false;
  v.volume = %.4f;
  return true;
})()`, volume)
}
