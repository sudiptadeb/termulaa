package com.debkosh.termulaa

import android.app.Application
import com.debkosh.termulaa.watch.WatchService
import com.debkosh.termulaa.work.CheckWorker
import kotlinx.coroutines.launch

class TermulaaApp : Application() {
    override fun onCreate() {
        super.onCreate()
        val graph = AppGraph.get(this)
        // (Re)schedule the periodic check and revive the watch service if the
        // toggle was left on (e.g. after a reboot or process death).
        graph.scope.launch {
            if (graph.store.serverUrlNow() != null) {
                CheckWorker.schedule(this@TermulaaApp, graph.store.pollMinutesNow())
                if (graph.store.watchEnabledNow()) WatchService.start(this@TermulaaApp)
            }
        }
    }
}
