package com.debkosh.termulaa.work

import android.content.Context
import androidx.work.Constraints
import androidx.work.CoroutineWorker
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.NetworkType
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import com.debkosh.termulaa.AppGraph
import java.util.concurrent.TimeUnit

/** The 15/30/60-min periodic background check. */
class CheckWorker(
    context: Context,
    params: WorkerParameters,
) : CoroutineWorker(context, params) {

    override suspend fun doWork(): Result {
        val graph = AppGraph.get(applicationContext)
        return when (graph.checkRunner.runOnce()) {
            is CheckRunner.RunResult.NetworkError -> Result.retry()
            else -> Result.success()
        }
    }

    companion object {
        private const val UNIQUE_NAME = "termulaa-check"

        /** Idempotent (re)schedule; call at app start and on interval change. */
        fun schedule(context: Context, intervalMinutes: Int) {
            val request = PeriodicWorkRequestBuilder<CheckWorker>(
                intervalMinutes.toLong(), TimeUnit.MINUTES,
            )
                .setConstraints(
                    Constraints.Builder().setRequiredNetworkType(NetworkType.CONNECTED).build()
                )
                .build()
            WorkManager.getInstance(context).enqueueUniquePeriodicWork(
                UNIQUE_NAME, ExistingPeriodicWorkPolicy.UPDATE, request,
            )
        }

        fun cancel(context: Context) {
            WorkManager.getInstance(context).cancelUniqueWork(UNIQUE_NAME)
        }
    }
}
